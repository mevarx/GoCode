package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/mevarx/GoCode/internal/pubsub"
	"github.com/mevarx/GoCode/internal/safetext"
)

// ApprovalChoice is the user's answer to an approval prompt. It is richer
// than a bool so a UI can offer once-only, session-scoped, and edit paths.
type ApprovalChoice int

const (
	// ApprovalDeny rejects this invocation.
	ApprovalDeny ApprovalChoice = iota
	// ApprovalOnce approves this invocation only.
	ApprovalOnce
	// ApprovalGrantExact approves this invocation and remembers its keyed
	// grant (tool, action, directory, grant key) for the life of this gate.
	ApprovalGrantExact
	// ApprovalReadOnlyTurn allows tools that declare themselves read-only for
	// the rest of the current turn.
	ApprovalReadOnlyTurn
	// ApprovalEdit asks the gate to collect edited arguments and re-prompt.
	ApprovalEdit
)

// Approved reports whether the choice approves the current invocation. The
// read-only flag itself is not an approval of the pending call: the gate
// re-checks whether that particular tool is read-only.
func (c ApprovalChoice) Approved() bool {
	return c == ApprovalOnce || c == ApprovalGrantExact
}

// ReadOnlyTool is implemented by tools that are safe to auto-approve for a
// turn after the user chooses "read-only tools this turn". Built-in read-only
// tools do not prompt at all; the interface is for MCP and future tools that
// require approval even when they only read.
type ReadOnlyTool interface {
	ReadOnly() bool
}

// errApprovalCancelled marks the cancellation path through decide so prepare
// can report a cancelled denial (distinct from a hook error).
var errApprovalCancelled = errors.New("approval cancelled")

// promptInfo carries the server-side identity of one approval request.
type promptInfo struct {
	sessionID  string
	toolCallID string
	toolName   string
	action     string
}

type ApprovalGate struct {
	autoApprove map[string]bool
	deny        map[string]bool
	// grants holds keyed session grants (PermissionKey). In-memory only,
	// never persisted, cleared on exit. Shell tools narrow to the command;
	// every other tool keeps the canonical exact-args grant class.
	grants map[PermissionKey]bool
	// allowReadOnly is the per-turn "read-only tools this turn" flag. BeginTurn
	// clears it at the start of every user turn.
	allowReadOnly bool
	// mode is the prompt policy (normal/yolo). The deny-list outranks it.
	mode PermissionMode
	// dangerTools is the configurable per-tool danger list.
	dangerTools map[string]bool
	// workspaceRoot is the containment base for classification and path keys.
	workspaceRoot string
	// sessionID is the session whose grants this gate remembers.
	sessionID string
	// autoApproveSessions marks never-prompting sessions (headless opt-in,
	// future subagent inheritance).
	autoApproveSessions map[string]bool
	// hookApprovals holds one-shot per-call hook approvals, keyed by
	// toolCallID. Consumed exactly once by prepareCall; cleared every turn.
	hookApprovals map[string]bool
	// pending holds open prompts by id for first-answer-wins resolution.
	pending map[string]*pendingRequest
	// scopes holds one prompt slot per scope (default: a single global slot).
	scopes map[string]chan struct{}
	mu     sync.RWMutex

	// SessionScope maps a session to a prompt queue. Nil means
	// the default GLOBAL scope: one prompt at a time app-wide. Different
	// scopes never wait on each other.
	SessionScope func(ctx context.Context, sessionID string) string
	// Bus, when set, receives permission_request / permission_notification /
	// permission_mode_changed payloads (must-deliver on the bus). Nil-safe.
	Bus PermissionBus
	// Observer is the async PromptShown/PromptResolved tap. It
	// runs off the requesting goroutine and cannot delay or alter a decision.
	Observer PromptObserver
	// OnPresentPrompt is the richest presentation hook: it receives the
	// request id and the danger reason. When set it takes precedence over
	// OnPresentChoice and OnPresent.
	OnPresentPrompt func(ApprovalPrompt) (ApprovalChoice, error)
	OnPresent       func(toolName string, args json.RawMessage, preview string) (bool, error)
	// OnPresentChoice is the choice-aware presentation hook. When set it takes
	// precedence over OnPresent, letting a UI return grant/read-only/edit
	// choices instead of a plain yes/no.
	OnPresentChoice func(toolName string, args json.RawMessage, preview string) (ApprovalChoice, error)
	// EditArgs collects replacement arguments when the user chooses edit. The
	// gate validates the result, regenerates the preview when it has one, and
	// re-prompts exactly once. Nil means editing is unavailable.
	EditArgs func(toolName string, args json.RawMessage) (json.RawMessage, error)
	// OnDecision, when set, observes every approval outcome: auto-approved,
	// denied by policy, and user-decided. It cannot alter the decision; the
	// loop uses it for per-turn metrics. Nil means no observer.
	OnDecision func(toolName string, approved bool, auto bool)

	// inputReader shares the caller's stdin reader; a second scanner would swallow piped answers.
	inputReader *bufio.Reader
	muInput     sync.Mutex
}

// SetInputReader makes the gate read answers from r. Callers sharing the stream must pass their reader.
func (g *ApprovalGate) SetInputReader(r *bufio.Reader) {
	g.muInput.Lock()
	defer g.muInput.Unlock()
	g.inputReader = r
}

func (g *ApprovalGate) readLine() (string, error) {
	g.muInput.Lock()
	r := g.inputReader
	g.muInput.Unlock()

	if r != nil {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return line, nil
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

func NewApprovalGate() *ApprovalGate {
	return &ApprovalGate{
		autoApprove:         make(map[string]bool),
		deny:                make(map[string]bool),
		grants:              make(map[PermissionKey]bool),
		mode:                ModeNormal,
		dangerTools:         make(map[string]bool),
		autoApproveSessions: make(map[string]bool),
		pending:             make(map[string]*pendingRequest),
		scopes:              make(map[string]chan struct{}),
	}
}

// BeginTurn clears the per-turn read-only flag and any unconsumed per-call
// hook approvals: a hook approval is per-call only and never outlives
// the turn that set it. Session grants intentionally survive: they are scoped
// to the gate, not to a turn. RunTurn calls this at the top of every user turn
// so the flag never outlives the turn that set it.
func (g *ApprovalGate) BeginTurn() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allowReadOnly = false
	g.hookApprovals = nil
}

// canonicalArgs normalizes JSON so equivalent encodings (key order,
// whitespace) produce one grant key. Invalid JSON falls back to raw bytes,
// which can only make matching stricter.
func canonicalArgs(args json.RawMessage) []byte {
	if len(args) == 0 || !json.Valid(args) {
		return append([]byte(nil), args...)
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	// Keep numeric literals exact; float64 would collapse distinct big ints.
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return append([]byte(nil), args...)
	}
	normalized, err := json.Marshal(v)
	if err != nil {
		return append([]byte(nil), args...)
	}
	return normalized
}

// SessionGrantCount reports how many keyed grants this gate holds.
func (g *ApprovalGate) SessionGrantCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.grants)
}

func (g *ApprovalGate) setAllowReadOnly() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allowReadOnly = true
}

func (g *ApprovalGate) readOnlyTurnAllowed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.allowReadOnly
}

// toolReadOnly reports whether the specific tool opted into turn-scoped
// auto-approval. Only the interface method decides; the tool name does not.
func toolReadOnly(tool Tool) bool {
	ro, ok := tool.(ReadOnlyTool)
	return ok && ro.ReadOnly()
}

func NewApprovalGateWithPermissions(autoApprove, deny []string) *ApprovalGate {
	g := NewApprovalGate()
	g.SetPermissions(autoApprove, deny)
	return g
}

func (g *ApprovalGate) SetPermissions(autoApprove, deny []string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.autoApprove = make(map[string]bool, len(autoApprove))
	for _, name := range autoApprove {
		g.autoApprove[strings.TrimSpace(name)] = true
	}

	g.deny = make(map[string]bool, len(deny))
	for _, name := range deny {
		g.deny[strings.TrimSpace(name)] = true
	}
}

// IsAutoApproved reports whether the bare tool name is allowlisted (every
// action of that tool). "tool:action" entries are checked by
// isAutoApprovedAction with the call's action.
func (g *ApprovalGate) IsAutoApproved(toolName string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.autoApprove[toolName]
}

// isAutoApprovedAction matches config allowlist entries of the form "tool"
// (every action) or "tool:action" (one action) — allowedTools.
func (g *ApprovalGate) isAutoApprovedAction(toolName, action string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.autoApprove[toolName] {
		return true
	}
	return action != "" && g.autoApprove[toolName+":"+action]
}

func (g *ApprovalGate) IsDenied(toolName string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.deny[toolName]
}

// RequestApproval is the legacy yes/no entry point. It accepts the same keys
// as the full prompt; a grant it records has the same session scope. Mode and
// keyed grants apply here too, and the deny-list still outranks everything.
func (g *ApprovalGate) RequestApproval(toolName string, args json.RawMessage, preview string) (bool, error) {
	if g.IsDenied(toolName) {
		return false, nil
	}
	if mode := g.Mode(); mode.SkipsPrompts() {
		danger, _ := g.classify(toolName, args)
		if mode.RefusesDangerous() && danger {
			return false, nil
		}
		return true, nil
	}
	if g.IsAutoApproved(toolName) {
		return true, nil
	}
	info := promptInfo{sessionID: g.currentSessionID(), toolName: toolName}
	if g.hasGrant(g.keyFor(info.sessionID, toolName, info.action, args)) {
		return true, nil
	}

	choice, finalArgs, err := g.decide(context.Background(), info, args, preview, nil)
	if err != nil {
		return false, err
	}
	switch choice {
	case ApprovalGrantExact:
		g.addGrant(g.keyFor(info.sessionID, toolName, info.action, finalArgs))
		return true, nil
	case ApprovalReadOnlyTurn:
		// Without a tool we cannot tell whether this call is read-only, so
		// only the flag is set and the pending call stays denied.
		g.setAllowReadOnly()
		return false, nil
	default:
		return choice.Approved(), nil
	}
}

// decide presents the prompt, applies at most one edit pass, and returns the
// choice with the SERVER-stored arguments it applies to. previewFor
// regenerates the preview after an edit; nil keeps the original preview
// (legacy callers).
//
// One pending request spans the whole decision (including the edit re-prompt,
// which re-publishes the SAME id with updated server-stored parameters). The
// lifecycle is: requested notification → request → [edit → request]* →
// granted/denied/cancelled notification, each transition published exactly
// once. Resolution is atomic: the first terminal answer wins.
func (g *ApprovalGate) decide(ctx context.Context, info promptInfo, args json.RawMessage, preview string, previewFor func(json.RawMessage) (string, error)) (ApprovalChoice, json.RawMessage, error) {
	danger, reason := g.classify(info.toolName, args)
	if !danger {
		reason = ""
	}
	key := g.keyFor(info.sessionID, info.toolName, info.action, args)
	p := &pendingRequest{
		gate:       g,
		id:         newPermissionID(),
		toolName:   info.toolName,
		sessionID:  info.sessionID,
		toolCallID: info.toolCallID,
		args:       append(json.RawMessage(nil), args...),
		preview:    preview,
		danger:     reason,
		key:        key,
		respCh:     make(chan promptAnswer, 1),
	}
	p.req = pubsub.PermissionRequest{
		ID:           p.id,
		Tool:         info.toolName,
		Action:       info.action,
		Args:         p.args,
		Description:  preview,
		Danger:       danger,
		DangerReason: reason,
		SessionID:    info.sessionID,
		ToolCallID:   info.toolCallID,
		Path:         key.Path,
		GrantKey:     key.GrantKey,
	}
	g.registerPending(p)

	// "prompt requested" (all flags false), then the request itself.
	g.publishNotification(ctx, pubsub.PermissionNotification{ID: p.id, Tool: info.toolName})
	g.publishRequest(ctx, p.snapshot())
	g.fireShown(p.snapshot())

	currentArgs := args
	currentPreview := preview
	edited := false

	for {
		ans := g.awaitAnswer(ctx, p)
		if ans.err != nil {
			return ApprovalDeny, currentArgs, ans.err
		}
		if ans.cancelled {
			return ApprovalDeny, currentArgs, fmt.Errorf("%w: %w", errApprovalCancelled, context.Canceled)
		}
		if ans.choice != ApprovalEdit {
			// The winning answer was resolved atomically; the executed args
			// are the server-stored ones, never a client echo.
			return ans.choice, p.currentArgs(), nil
		}

		// Edit pass: gate-controlled, at most once, server-stored params.
		// An external resolve may have won while the edit was collected;
		// a closed request is never edited or re-presented.
		if p.isClosed() {
			continue
		}
		if g.EditArgs == nil {
			err := fmt.Errorf("editing arguments is not supported by this interface")
			p.resolve(ApprovalDeny, err, false)
			return ApprovalDeny, currentArgs, err
		}
		if edited {
			err := fmt.Errorf("arguments may be edited only once per request")
			p.resolve(ApprovalDeny, err, false)
			return ApprovalDeny, currentArgs, err
		}
		editedArgs, err := g.EditArgs(info.toolName, currentArgs)
		if err != nil {
			err = fmt.Errorf("invalid edited arguments: %w", err)
			p.resolve(ApprovalDeny, err, false)
			return ApprovalDeny, currentArgs, err
		}
		if !json.Valid(editedArgs) {
			err = fmt.Errorf("invalid edited arguments: not valid JSON")
			p.resolve(ApprovalDeny, err, false)
			return ApprovalDeny, currentArgs, err
		}
		edited = true
		currentArgs = editedArgs
		if previewFor != nil {
			regenerated, err := previewFor(currentArgs)
			if err != nil {
				err = fmt.Errorf("preview error: %w", err)
				p.resolve(ApprovalDeny, err, false)
				return ApprovalDeny, currentArgs, err
			}
			currentPreview = regenerated
		}
		// Re-classify and re-key the EDITED args, update the server-stored
		// request, and re-present under the same id.
		d2, r2 := g.classify(info.toolName, currentArgs)
		if !d2 {
			r2 = ""
		}
		k2 := g.keyFor(info.sessionID, info.toolName, info.action, currentArgs)
		p.update(currentArgs, currentPreview, r2, k2)
		if p.isClosed() {
			continue
		}
		g.publishRequest(ctx, p.snapshot())
		g.fireShown(p.snapshot())
		// Without a regenerating previewer the next render still shows the
		// edited arguments; only the preview body stays from before the edit.
	}
}

// awaitAnswer runs the presentation hook off the requesting goroutine and
// waits for the first terminal answer or cancellation. The hook's answer is
// routed through the SAME atomic resolve as external Resolve calls, so a UI
// answer and a bus responder race fairly and exactly one wins. On ctx.Done
// the request resolves Cancelled (or reports the answer that won the race).
//
// A hook still blocked after cancellation (an open modal, a parked stdin
// read) finishes later into a no-op resolve; its goroutine then exits. The
// terminal prompt's stdin read cannot be interrupted mid-line — the shared
// reader keeps piped answers in order for the next prompt.
func (g *ApprovalGate) awaitAnswer(ctx context.Context, p *pendingRequest) promptAnswer {
	// A request already resolved (e.g. an external Resolve won during the
	// edit pass) returns its stored outcome without re-presenting the hook.
	if p.isClosed() {
		return p.outcome()
	}
	snap := p.snapshot()
	go func() {
		choice, err := g.presentHook(snap)
		if err == nil && choice == ApprovalEdit {
			p.editAnswer()
			return
		}
		p.resolve(choice, err, false)
	}()
	select {
	case ans := <-p.respCh:
		return ans
	case <-ctx.Done():
		p.resolve(ApprovalDeny, nil, true)
		return p.outcome()
	}
}

// presentHook routes to the richest configured hook, otherwise renders the
// terminal prompt. Untrusted tool text is sanitized before any path.
func (g *ApprovalGate) presentHook(snap pubsub.PermissionRequest) (ApprovalChoice, error) {
	// Model/tool text is untrusted; strip escapes before it can repaint the gate.
	displayName := safetext.Sanitize(snap.Tool)
	displayPreview := safetext.Sanitize(snap.Description)
	displayDanger := safetext.Sanitize(snap.DangerReason)

	if g.OnPresentPrompt != nil {
		return g.OnPresentPrompt(ApprovalPrompt{
			ID:       snap.ID,
			ToolName: displayName,
			Args:     snap.Args,
			Preview:  displayPreview,
			Danger:   displayDanger,
		})
	}
	if g.OnPresentChoice != nil {
		return g.OnPresentChoice(displayName, snap.Args, displayPreview)
	}
	if g.OnPresent != nil {
		approved, err := g.OnPresent(displayName, snap.Args, displayPreview)
		if err != nil {
			return ApprovalDeny, err
		}
		if approved {
			return ApprovalOnce, nil
		}
		return ApprovalDeny, nil
	}
	return g.promptTerminal(displayName, snap.Args, displayPreview, displayDanger)
}

func (g *ApprovalGate) promptTerminal(displayName string, args json.RawMessage, displayPreview string, danger string) (ApprovalChoice, error) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("🔧 Tool: %s\n", displayName)
	if danger != "" {
		fmt.Printf("⚠ DANGEROUS: %s\n", danger)
	}
	fmt.Println(strings.Repeat("─", 50))

	var prettyArgs map[string]interface{}
	if err := json.Unmarshal(args, &prettyArgs); err == nil {
		for k, v := range prettyArgs {
			fmt.Printf("  %s: %v\n", safetext.Sanitize(k), safetext.Sanitize(fmt.Sprintf("%v", v)))
		}
	} else {
		fmt.Printf("  args: %s\n", safetext.Sanitize(string(args)))
	}

	if displayPreview != "" {
		fmt.Println()
		// Delimiters stop injected text from spoofing the prompt line below.
		fmt.Println("─── preview begin ───")
		fmt.Println(displayPreview)
		fmt.Println("─── preview end ───")
	}

	fmt.Println(strings.Repeat("─", 50))
	fmt.Print("Approve? [y]es / [a]llow exact args this session / [r]ead-only tools this turn / [e]dit / [n]o: ")

	line, err := g.readLine()
	if err != nil {
		return ApprovalDeny, fmt.Errorf("failed to read input: %w", err)
	}
	return parseApprovalResponse(line), nil
}

// parseApprovalResponse maps a typed answer to a choice. Anything unrecognized
// is a denial, matching the old default-no behavior.
func parseApprovalResponse(line string) ApprovalChoice {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return ApprovalOnce
	case "a", "all", "allow":
		return ApprovalGrantExact
	case "r", "read", "readonly", "read-only":
		return ApprovalReadOnlyTurn
	case "e", "edit":
		return ApprovalEdit
	default:
		return ApprovalDeny
	}
}

// WrapExecution runs preview → approval → execute. Denied operations never call Execute.
func (g *ApprovalGate) WrapExecution(ctx context.Context, tool Tool, args json.RawMessage) (Result, error) {
	execute, err := g.Prepare(ctx, tool, args)
	if err != nil {
		return redactToolResult(Result{}, err)
	}
	result, err := execute(ctx)
	return redactToolResult(result, err)
}

// Prepare runs the whole decision flow — permissions, mode, session grants,
// the per-turn read-only flag, preview, and the user prompt — and returns a
// closure that runs the approved call with the final arguments. A denial
// returns a closure that reports the denial Result and never executes the tool.
//
// Running every decision before any execution lets a caller prompt for a
// whole batch in a deterministic order and then execute independent calls
// concurrently. Prepare only mutates gate state (grants, read-only flag,
// decision observers); the closure only runs the tool.
func (g *ApprovalGate) Prepare(ctx context.Context, tool Tool, args json.RawMessage) (func(context.Context) (Result, error), error) {
	return g.prepareCall(ctx, tool, args, "")
}

// PrepareWithID is Prepare with the model's tool-call id, carried onto the
// bus PermissionRequest so observers can correlate prompt and call.
func (g *ApprovalGate) PrepareWithID(ctx context.Context, tool Tool, args json.RawMessage, toolCallID string) (func(context.Context) (Result, error), error) {
	return g.prepareCall(ctx, tool, args, toolCallID)
}

// notifyDecision reports an outcome to the optional observer. Observation
// only: it cannot change whether a tool runs.
func (g *ApprovalGate) notifyDecision(toolName string, approved, auto bool) {
	if g.OnDecision != nil {
		g.OnDecision(toolName, approved, auto)
	}
}

// redactToolResult masks credentials in output, error and diff. It is the
// unconditional transcript boundary: every WrapExecution result passes through
// it, and no configuration can turn it off. [tools.shell] redact_secrets only
// affects ShellGuard masking of shell_exec output before that point; transcript
// and export redaction stay on regardless.
func redactToolResult(r Result, err error) (Result, error) {
	if err != nil {
		return r, err
	}
	var hits []string
	r.Output, hits = RedactToolOutput(r.Output)
	var errHits []string
	r.Error, errHits = RedactToolOutput(r.Error)
	var diffHits []string
	r.Diff, diffHits = RedactToolOutput(r.Diff)
	hits = append(hits, errHits...)
	hits = append(hits, diffHits...)
	if len(hits) > 0 {
		r.Output += DescribeRedactions(hits)
	}
	return r, err
}

// RedactToolResult applies the same transcript boundary to a result a caller
// executed itself from a Prepare closure. It cannot be turned off.
func RedactToolResult(r Result, err error) (Result, error) {
	return redactToolResult(r, err)
}

// prepareCall is the decision order:
//
//	deny-list → mode (yolo skip/refuse) → config auto_approve ("tool" or
//	"tool:action") → no-approval tools → per-call hook approval →
//	session auto-approve → keyed session grant → read-only turn flag →
//	preview → lock scope → re-check session auto-approve/grant/flag (a
//	preceding prompt may have granted) → requested notification → prompt →
//	block on ctx/response → granted/denied/cancelled.
//
// The deny-list ALWAYS wins over mode, grants, hook approvals and
// auto-approve. Cancellation resolves the request Cancelled and denies; it
// never hangs.
func (g *ApprovalGate) prepareCall(ctx context.Context, tool Tool, args json.RawMessage, toolCallID string) (func(context.Context) (Result, error), error) {
	toolName := tool.Spec().Name
	action := ToolAction(tool)
	sessionID := g.currentSessionID()
	info := promptInfo{sessionID: sessionID, toolCallID: toolCallID, toolName: toolName, action: action}

	// A denial is final: the returned closure reports it and never executes.
	stop := func(r Result) (func(context.Context) (Result, error), error) {
		return func(context.Context) (Result, error) { return r, nil }, nil
	}

	// 1. Deny-list: unconditional, outranks every mode and grant.
	if g.IsDenied(toolName) {
		g.notifyDecision(toolName, false, true)
		return stop(Result{Error: fmt.Sprintf("execution of tool %q is denied by permissions configuration", toolName)})
	}

	danger, reason := g.classify(toolName, args)

	// 2. Mode: yolo skips prompts for safe calls and REFUSES dangerous ones
	// without asking, naming the way out. Sysadmin is omitted by design.
	if mode := g.Mode(); mode.SkipsPrompts() {
		if mode.RefusesDangerous() && danger {
			g.notifyDecision(toolName, false, true)
			return stop(Result{Error: fmt.Sprintf("refused in yolo mode: %s; use normal mode or add an allow rule", reason)})
		}
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	// 3. Config allowlist: bare tool or tool:action.
	if g.isAutoApprovedAction(toolName, action) {
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	if !tool.RequiresApproval() {
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	// a per-call hook approval short-circuits the prompt for THIS call
	// only. The deny-list, mode and config
	// checks above already outrank it. The preview still runs so validation
	// and audit output match a prompted call, and a Granted notification is
	// published for audit. The stamp is consumed exactly once.
	if toolCallID != "" && hookApprovalFromContext(ctx) == toolCallID && g.consumeHookApproval(toolCallID) {
		if _, err := g.previewFor(ctx, tool, args); err != nil {
			return stop(Result{Error: fmt.Sprintf("preview error: %v", err)})
		}
		g.grantedNotification(toolName)
		g.notifyDecision(toolName, true, false)
		return executeClosure(tool, args), nil
	}

	// 4. Session auto-approve (headless opt-in / subagent inheritance).
	if g.sessionAutoApproved(sessionID) {
		g.grantedNotification(toolName)
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	// 5. Keyed session grant bypasses the prompt only. The preview still runs
	// so validation and audit output are identical to a prompted call, and
	// the result says so instead of bypassing silently.
	key := g.keyFor(sessionID, toolName, action, args)
	if g.hasGrant(key) {
		if _, err := g.previewFor(ctx, tool, args); err != nil {
			return stop(Result{Error: fmt.Sprintf("preview error: %v", err)})
		}
		g.grantedNotification(toolName)
		g.notifyDecision(toolName, true, false)
		return func(ctx context.Context) (Result, error) {
			result, err := executeToolRecovered(ctx, tool, args)
			return markSessionGrant(result), err
		}, nil
	}

	// The per-turn read-only flag only covers tools that opt in explicitly.
	if g.readOnlyTurnAllowed() && toolReadOnly(tool) {
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	previewStr, err := g.previewFor(ctx, tool, args)
	if err != nil {
		// Preview errors are validation errors — return them without executing.
		return stop(Result{Error: fmt.Sprintf("preview error: %v", err)})
	}

	// 6. Serialize prompts per scope (default global: one dialog app-wide).
	scope := g.scopeFor(ctx, sessionID)
	if err := g.lockScope(ctx, scope); err != nil {
		// Cancelled while queued: no prompt was shown, so only the terminal
		// Cancelled notification is published. Never hangs.
		g.publishNotification(ctx, pubsub.PermissionNotification{
			ID:        newPermissionID(),
			Tool:      toolName,
			Cancelled: true,
			Reason:    "cancelled before prompt",
		})
		g.notifyDecision(toolName, false, false)
		return stop(Result{Output: fmt.Sprintf("User denied execution of %s (cancelled)", toolName)})
	}
	defer g.unlockScope(scope)

	// Re-check under the scope lock: a prompt ahead of this one may have
	// recorded a grant, set the read-only flag, or auto-approved the session.
	if g.sessionAutoApproved(sessionID) {
		g.grantedNotification(toolName)
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}
	if g.hasGrant(key) {
		g.grantedNotification(toolName)
		g.notifyDecision(toolName, true, false)
		return func(ctx context.Context) (Result, error) {
			result, err := executeToolRecovered(ctx, tool, args)
			return markSessionGrant(result), err
		}, nil
	}
	if g.readOnlyTurnAllowed() && toolReadOnly(tool) {
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	chooseWithRegeneratedPreview := func(edited json.RawMessage) (string, error) {
		return g.previewFor(ctx, tool, edited)
	}
	choice, finalArgs, err := g.decide(ctx, info, args, previewStr, chooseWithRegeneratedPreview)
	if err != nil {
		g.notifyDecision(toolName, false, false)
		if errors.Is(err, errApprovalCancelled) {
			return stop(Result{Output: fmt.Sprintf("User denied execution of %s (cancelled)", toolName)})
		}
		return stop(Result{Error: fmt.Sprintf("approval error: %v", err)})
	}

	switch choice {
	case ApprovalDeny:
		g.notifyDecision(toolName, false, false)
		return stop(Result{Output: fmt.Sprintf("User denied execution of %s", toolName)})

	case ApprovalReadOnlyTurn:
		g.setAllowReadOnly()
		if toolReadOnly(tool) {
			g.notifyDecision(toolName, true, false)
			return executeClosure(tool, finalArgs), nil
		}
		g.notifyDecision(toolName, false, false)
		return stop(Result{Output: fmt.Sprintf("User denied execution of %s (only read-only tools are allowed this turn)", toolName)})

	case ApprovalGrantExact:
		// The grant key is recomputed from the SERVER-stored final args
		// (possibly edited by the gate's own edit pass) — never a client echo.
		g.addGrant(g.keyFor(sessionID, toolName, action, finalArgs))
		g.notifyDecision(toolName, true, false)
		return executeClosure(tool, finalArgs), nil

	default: // ApprovalOnce
		g.notifyDecision(toolName, true, false)
		return executeClosure(tool, finalArgs), nil
	}
}

// executeClosure defers the actual tool invocation so preparation can finish
// for every call in a batch before any of them run.
func executeClosure(tool Tool, args json.RawMessage) func(context.Context) (Result, error) {
	return func(ctx context.Context) (Result, error) {
		return executeToolRecovered(ctx, tool, args)
	}
}

// previewFor renders the description shown before approval. Tools without a
// Previewer have none.
func (g *ApprovalGate) previewFor(ctx context.Context, tool Tool, args json.RawMessage) (string, error) {
	previewer, ok := tool.(Previewer)
	if !ok {
		return "", nil
	}
	preview, err := previewToolRecovered(ctx, previewer, args)
	if err != nil {
		return "", err
	}
	return preview.String(), nil
}

// markSessionGrant records that the prompt was skipped because identical args
// were approved earlier this session. The note reaches the user and transcript.
func markSessionGrant(r Result) Result {
	const note = "(session grant) "
	if r.Output == "" {
		r.Output = "(session grant)"
	} else {
		r.Output = note + r.Output
	}
	return r
}

// executeToolRecovered converts a tool panic into an error so one tool cannot kill the process.
func executeToolRecovered(ctx context.Context, tool Tool, args json.RawMessage) (result Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = Result{Error: fmt.Sprintf("panic while executing %s: %v", tool.Spec().Name, r)}
			err = nil
		}
	}()
	return tool.Execute(ctx, args)
}

// previewToolRecovered is the same containment for the preview step.
func previewToolRecovered(ctx context.Context, previewer Previewer, args json.RawMessage) (preview Preview, err error) {
	defer func() {
		if r := recover(); r != nil {
			preview = Preview{}
			err = fmt.Errorf("panic while generating preview: %v", r)
		}
	}()
	return previewer.Preview(ctx, args)
}
