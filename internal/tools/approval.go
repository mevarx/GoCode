package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

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
	// ApprovalGrantExact approves this invocation and remembers the exact
	// arguments for the life of this gate.
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

type ApprovalGate struct {
	autoApprove map[string]bool
	deny        map[string]bool
	// grants holds sha256(toolName + "\x00" + canonicalArgs) for calls the user
	// explicitly allowed for the rest of the session. In-memory only, never
	// persisted, and narrower than autoApprove: different args re-prompt.
	grants map[string]bool
	// allowReadOnly is the per-turn "read-only tools this turn" flag. BeginTurn
	// clears it at the start of every user turn.
	allowReadOnly bool
	mu            sync.RWMutex
	OnPresent     func(toolName string, args json.RawMessage, preview string) (bool, error)
	// OnPresentChoice is the richer presentation hook. When set it takes
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
		autoApprove: make(map[string]bool),
		deny:        make(map[string]bool),
		grants:      make(map[string]bool),
	}
}

// BeginTurn clears the per-turn read-only flag. Session grants intentionally
// survive: they are scoped to the gate, not to a turn. RunTurn calls this at
// the top of every user turn so the flag never outlives the turn that set it.
func (g *ApprovalGate) BeginTurn() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allowReadOnly = false
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

// grantKey identifies one exact invocation. Tool name and arguments are
// length-independent through a SHA-256 digest.
func grantKey(toolName string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0})
	h.Write(canonicalArgs(args))
	return string(h.Sum(nil))
}

func (g *ApprovalGate) hasGrant(toolName string, args json.RawMessage) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.grants[grantKey(toolName, args)]
}

func (g *ApprovalGate) addGrant(toolName string, args json.RawMessage) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grants[grantKey(toolName, args)] = true
}

// SessionGrantCount reports how many exact-argument grants this gate holds.
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

func (g *ApprovalGate) IsAutoApproved(toolName string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.autoApprove[toolName]
}

func (g *ApprovalGate) IsDenied(toolName string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.deny[toolName]
}

// RequestApproval is the legacy yes/no entry point. It accepts the same keys
// as the full prompt; a grant it records has the same session scope.
func (g *ApprovalGate) RequestApproval(toolName string, args json.RawMessage, preview string) (bool, error) {
	if g.IsDenied(toolName) {
		return false, nil
	}
	if g.IsAutoApproved(toolName) {
		return true, nil
	}

	choice, finalArgs, err := g.decide(toolName, args, preview, nil)
	if err != nil {
		return false, err
	}
	switch choice {
	case ApprovalGrantExact:
		g.addGrant(toolName, finalArgs)
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
// choice with the arguments it applies to. previewFor regenerates the preview
// after an edit; nil keeps the original preview (legacy callers).
func (g *ApprovalGate) decide(toolName string, args json.RawMessage, preview string, previewFor func(json.RawMessage) (string, error)) (ApprovalChoice, json.RawMessage, error) {
	currentArgs := args
	currentPreview := preview
	edited := false

	for {
		choice, err := g.present(toolName, currentArgs, currentPreview)
		if err != nil {
			return ApprovalDeny, currentArgs, err
		}
		if choice != ApprovalEdit {
			return choice, currentArgs, nil
		}
		if g.EditArgs == nil {
			return ApprovalDeny, currentArgs, fmt.Errorf("editing arguments is not supported by this interface")
		}
		if edited {
			return ApprovalDeny, currentArgs, fmt.Errorf("arguments may be edited only once per request")
		}
		editedArgs, err := g.EditArgs(toolName, currentArgs)
		if err != nil {
			return ApprovalDeny, currentArgs, fmt.Errorf("invalid edited arguments: %w", err)
		}
		if !json.Valid(editedArgs) {
			return ApprovalDeny, currentArgs, fmt.Errorf("invalid edited arguments: not valid JSON")
		}
		edited = true
		currentArgs = editedArgs
		if previewFor != nil {
			regenerated, err := previewFor(currentArgs)
			if err != nil {
				return ApprovalDeny, currentArgs, fmt.Errorf("preview error: %w", err)
			}
			currentPreview = regenerated
		}
		// Without a regenerating previewer the next render still shows the
		// edited arguments; only the preview body stays from before the edit.
	}
}

// present routes to the UI hook when configured, otherwise renders the
// terminal prompt. Untrusted tool text is sanitized before either path.
func (g *ApprovalGate) present(toolName string, args json.RawMessage, preview string) (ApprovalChoice, error) {
	// Model/tool text is untrusted; strip escapes before it can repaint the gate.
	displayName := safetext.Sanitize(toolName)
	displayPreview := safetext.Sanitize(preview)

	if g.OnPresentChoice != nil {
		return g.OnPresentChoice(displayName, args, displayPreview)
	}
	if g.OnPresent != nil {
		approved, err := g.OnPresent(displayName, args, displayPreview)
		if err != nil {
			return ApprovalDeny, err
		}
		if approved {
			return ApprovalOnce, nil
		}
		return ApprovalDeny, nil
	}
	return g.promptTerminal(displayName, args, displayPreview)
}

func (g *ApprovalGate) promptTerminal(displayName string, args json.RawMessage, displayPreview string) (ApprovalChoice, error) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("🔧 Tool: %s\n", displayName)
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

// Prepare runs the whole decision flow — permissions, session grants, the
// per-turn read-only flag, preview, and the user prompt — and returns a
// closure that runs the approved call with the final arguments. A denial
// returns a closure that reports the denial Result and never executes the tool.
//
// Running every decision before any execution lets a caller prompt for a
// whole batch in a deterministic order and then execute independent calls
// concurrently. Prepare only mutates gate state (grants, read-only flag,
// decision observers); the closure only runs the tool.
func (g *ApprovalGate) Prepare(ctx context.Context, tool Tool, args json.RawMessage) (func(context.Context) (Result, error), error) {
	return g.prepare(ctx, tool, args)
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

func (g *ApprovalGate) prepare(ctx context.Context, tool Tool, args json.RawMessage) (func(context.Context) (Result, error), error) {
	toolName := tool.Spec().Name

	// A denial is final: the returned closure reports it and never executes.
	stop := func(r Result) (func(context.Context) (Result, error), error) {
		return func(context.Context) (Result, error) { return r, nil }, nil
	}

	if g.IsDenied(toolName) {
		g.notifyDecision(toolName, false, true)
		return stop(Result{Error: fmt.Sprintf("execution of tool %q is denied by permissions configuration", toolName)})
	}

	if g.IsAutoApproved(toolName) {
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	if !tool.RequiresApproval() {
		g.notifyDecision(toolName, true, true)
		return executeClosure(tool, args), nil
	}

	// An exact-argument session grant bypasses the prompt only. The preview
	// still runs so validation and audit output are identical to a prompted
	// call, and the result says so instead of bypassing silently.
	if g.hasGrant(toolName, args) {
		if _, err := g.previewFor(ctx, tool, args); err != nil {
			return stop(Result{Error: fmt.Sprintf("preview error: %v", err)})
		}
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

	chooseWithRegeneratedPreview := func(edited json.RawMessage) (string, error) {
		return g.previewFor(ctx, tool, edited)
	}
	choice, finalArgs, err := g.decide(toolName, args, previewStr, chooseWithRegeneratedPreview)
	if err != nil {
		g.notifyDecision(toolName, false, false)
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
		g.addGrant(toolName, finalArgs)
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
