package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mevarx/GoCode/internal/pubsub"
)

// Permission service vocabulary. The gate is a
// request/grant service: modes decide whether prompts happen at all, keyed
// session grants remember decisions in memory, prompts serialize per scope,
// and exactly one answer resolves each request (first-answer-wins) using the
// SERVER-stored parameters — a responder only ever echoes the request id and
// a choice, so it cannot widen a grant.
//
// sysadmin mode is deliberately OMITTED: a nothing-blocked mode contradicts
// GoCode's unconditional deny-list/redaction invariant; normal and yolo are
// the only modes. The deny-list outranks every mode and grant.

// PermissionMode selects the prompt policy.
type PermissionMode string

const (
	// ModeNormal prompts for approval-gated calls (the historical flow).
	ModeNormal PermissionMode = "normal"
	// ModeYolo skips prompts for non-dangerous calls and REFUSES dangerous
	// calls without asking, naming the way out in the tool result.
	ModeYolo PermissionMode = "yolo"
)

// Valid reports whether m is a known mode.
func (m PermissionMode) Valid() bool { return m == ModeNormal || m == ModeYolo }

// SkipsPrompts reports whether the mode answers approval-gated calls without
// a user prompt.
func (m PermissionMode) SkipsPrompts() bool { return m == ModeYolo }

// RefusesDangerous reports whether dangerous calls are refused outright
// (yolo only; normal prompts with a danger warning instead).
func (m PermissionMode) RefusesDangerous() bool { return m == ModeYolo }

// PermissionKey identifies one remembered session grant.
// In-memory only, cleared on process exit. Path is normalized to a DIRECTORY
// (file → its dir, "." → the workspace). GrantKey narrows the grant: shell
// tools use the command string (approving ls does not approve sudo); every
// other tool uses the canonical exact arguments, preserving the historical
// exact-args grant class.
type PermissionKey struct {
	SessionID string
	ToolName  string
	Action    string
	Path      string
	GrantKey  string
}

// ActionTool is implemented by tools that expose a sub-action for
// "tool:action" permission matching. Tools without it have an empty action.
type ActionTool interface {
	Action() string
}

// ToolAction reports the action component for a tool: the ActionTool
// interface when implemented, otherwise the documented built-in mapping.
// MCP and future tools opt in via the interface.
func ToolAction(tool Tool) string {
	if at, ok := tool.(ActionTool); ok {
		return at.Action()
	}
	switch tool.Spec().Name {
	case "shell_exec":
		return "exec"
	case "file_read":
		return "read"
	case "file_write":
		return "write"
	case "file_patch":
		return "patch"
	case "code_search":
		return "search"
	}
	return ""
}

// PermissionBus is the gate's view of the event bus. The agent loop
// implements it by publishing LoopEvents; the gate never imports the agent
// package. All publishes are nil-safe and must-deliver on the bus side.
type PermissionBus interface {
	PublishPermissionRequest(ctx context.Context, req pubsub.PermissionRequest)
	PublishPermissionNotification(ctx context.Context, n pubsub.PermissionNotification)
	PublishPermissionModeChanged(ctx context.Context, change pubsub.PermissionModeChanged)
}

// PromptOutcome is the terminal state of a permission prompt.
type PromptOutcome int

const (
	PromptGranted PromptOutcome = iota
	PromptDenied
	PromptCancelled
)

func (o PromptOutcome) String() string {
	switch o {
	case PromptGranted:
		return "granted"
	case PromptDenied:
		return "denied"
	case PromptCancelled:
		return "cancelled"
	}
	return "unknown"
}

// PromptObserver is the async audit tap. Both methods run OFF the
// requesting goroutine, so a slow observer never delays the tool. hooks
// attach here. Panics inside an observer are contained.
type PromptObserver interface {
	PromptShown(req pubsub.PermissionRequest)
	PromptResolved(req pubsub.PermissionRequest, outcome PromptOutcome)
}

// ApprovalPrompt is the full presentation payload handed to OnPresentPrompt.
// Danger is empty for non-dangerous calls, otherwise the classifier reason.
type ApprovalPrompt struct {
	ID       string
	ToolName string
	Args     json.RawMessage
	Preview  string
	Danger   string
}

// newPermissionID returns an unguessable UUID-shaped hex id (crypto/rand,
// stdlib only — no dependency). The id is the ONLY thing a responder echoes.
func newPermissionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; a zero id would
		// still be unique per pending map entry, but refuse to guess.
		panic(fmt.Sprintf("permission id: crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// normalizePermissionPath maps a raw path argument to its grant DIRECTORY
// a file becomes its parent dir, "." becomes the workspace, a
// directory stays itself, and a not-yet-existing path (a new file) is treated
// as a file. Relative paths resolve against workspaceRoot (the process
// working directory when unset). Empty stays empty: tools without a path
// argument have no path component in their key.
func normalizePermissionPath(raw, workspaceRoot string) string {
	if raw == "" {
		return ""
	}
	base := workspaceRoot
	if base == "" {
		if wd, err := os.Getwd(); err == nil {
			base = wd
		}
	}
	cleaned := strings.ReplaceAll(raw, "\\", string(filepath.Separator))
	var abs string
	if filepath.IsAbs(cleaned) {
		abs = filepath.Clean(cleaned)
	} else if base != "" {
		abs = filepath.Clean(filepath.Join(base, cleaned))
	} else {
		abs = filepath.Clean(cleaned)
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return abs
	}
	return filepath.Dir(abs)
}

// promptAnswer is the resolved outcome delivered to the waiting request.
type promptAnswer struct {
	choice    ApprovalChoice
	err       error
	cancelled bool
}

// pendingRequest is the SERVER-stored state of one open prompt. Responders
// echo only the id; args/preview/key live here and are mutated only by the
// gate's own edit pass, so a dishonest responder cannot widen a grant.
// Resolution is atomic: the first terminal answer wins, later ones are no-ops.
type pendingRequest struct {
	gate       *ApprovalGate
	id         string
	toolName   string
	sessionID  string
	toolCallID string

	mu        sync.Mutex
	closed    bool
	args      json.RawMessage
	preview   string
	danger    string
	key       PermissionKey
	req       pubsub.PermissionRequest
	choice    ApprovalChoice
	err       error
	cancelled bool

	respCh chan promptAnswer // cap 1
	once   sync.Once         // terminal notification + observer fire exactly once
}

func (p *pendingRequest) snapshot() pubsub.PermissionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.req
}

func (p *pendingRequest) currentArgs() json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(json.RawMessage(nil), p.args...)
}

// update replaces the server-stored parameters after the gate's edit pass.
func (p *pendingRequest) update(args json.RawMessage, preview, danger string, key PermissionKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.args = append(json.RawMessage(nil), args...)
	p.preview = preview
	p.danger = danger
	p.key = key
	p.req.Args = p.args
	p.req.Description = preview
	p.req.Danger = danger != ""
	p.req.DangerReason = danger
	p.req.Path = key.Path
	p.req.GrantKey = key.GrantKey
}

// resolve atomically closes the request. The first terminal answer wins;
// later answers (including a late answer after cancellation) return false
// and change nothing. The lifecycle notification and the observer tap fire
// exactly once, inside the win.
func (p *pendingRequest) resolve(choice ApprovalChoice, err error, cancelled bool) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.closed = true
	p.choice, p.err, p.cancelled = choice, err, cancelled
	snap := p.req
	p.mu.Unlock()

	p.gate.deletePending(p.id)

	n := pubsub.PermissionNotification{ID: p.id, Tool: p.toolName}
	outcome := PromptDenied
	switch {
	case cancelled:
		n.Cancelled = true
		n.Reason = "cancelled before a decision"
		outcome = PromptCancelled
	case err != nil:
		n.Denied = true
		n.Reason = err.Error()
	case choice.Approved() || choice == ApprovalReadOnlyTurn:
		n.Granted = true
		outcome = PromptGranted
	default:
		n.Denied = true
	}
	p.gate.publishNotification(context.Background(), n)
	p.gate.fireResolved(snap, outcome)

	p.once.Do(func() {
		p.respCh <- promptAnswer{choice: choice, err: err, cancelled: cancelled}
	})
	return true
}

// editAnswer delivers the non-terminal edit choice without closing the
// request: the gate's edit pass re-presents the SAME request id with updated
// server-stored parameters. A closed (resolved/cancelled) request drops it.
func (p *pendingRequest) editAnswer() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.respCh <- promptAnswer{choice: ApprovalEdit}
	return true
}

// outcome reads the winning answer after a resolve attempt.
func (p *pendingRequest) outcome() promptAnswer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return promptAnswer{choice: p.choice, err: p.err, cancelled: p.cancelled}
}

// isClosed reports whether a terminal answer already won.
func (p *pendingRequest) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// --- gate fields and methods for the permission service ---

// SetMode switches the prompt policy and publishes permission_mode_changed
// on the bus when the mode actually changes. Unknown modes are rejected.
func (g *ApprovalGate) SetMode(mode PermissionMode) error {
	if !mode.Valid() {
		return fmt.Errorf("unknown permission mode %q (want normal or yolo)", mode)
	}
	g.mu.Lock()
	old := g.mode
	g.mode = mode
	g.mu.Unlock()
	if old != mode {
		g.publishModeChanged(context.Background(), pubsub.PermissionModeChanged{
			Mode:     string(mode),
			Previous: string(old),
		})
	}
	return nil
}

// Mode reports the current prompt policy.
func (g *ApprovalGate) Mode() PermissionMode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mode
}

// SetDangerTools installs the configurable per-tool danger list
// ([permissions] dangerous_tools). Listed tools are always classified
// dangerous, on top of the pure classifier's rules.
func (g *ApprovalGate) SetDangerTools(names []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dangerTools = make(map[string]bool, len(names))
	for _, name := range names {
		g.dangerTools[strings.TrimSpace(name)] = true
	}
}

// SetWorkspaceRoot sets the containment base for danger classification and
// path normalization. The loop installs it at the start of every turn.
func (g *ApprovalGate) SetWorkspaceRoot(root string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.workspaceRoot = root
}

func (g *ApprovalGate) workspace() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.workspaceRoot
}

// SetSessionID binds the gate to the session whose grants it remembers.
// NewAgentLoop installs it; a gate without one uses the empty session id.
func (g *ApprovalGate) SetSessionID(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessionID = id
}

func (g *ApprovalGate) currentSessionID() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.sessionID
}

// AutoApproveSession marks a session as never-prompting: every
// approval-gated call that survives the deny-list, mode and config checks is
// granted with a Granted notification. Headless opt-in uses it; interactive
// sessions never set it.
func (g *ApprovalGate) AutoApproveSession(sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.autoApproveSessions[sessionID] = true
}

// InheritAutoApproval copies a parent session's auto-approval to a child
// (future subagents): otherwise a subagent would deadlock on a prompt nobody
// can answer. A parent without auto-approval grants nothing.
func (g *ApprovalGate) InheritAutoApproval(parent, child string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.autoApproveSessions[parent] {
		g.autoApproveSessions[child] = true
	}
}

func (g *ApprovalGate) sessionAutoApproved(sessionID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.autoApproveSessions[sessionID]
}

// SessionGrantKeys returns a copy of the remembered grant keys (inspection
// and tests; the footer renders tool names from the UI side).
func (g *ApprovalGate) SessionGrantKeys() []PermissionKey {
	g.mu.RLock()
	defer g.mu.RUnlock()
	keys := make([]PermissionKey, 0, len(g.grants))
	for k := range g.grants {
		keys = append(keys, k)
	}
	return keys
}

// keyFor builds the grant key for one invocation from SERVER-side inputs.
func (g *ApprovalGate) keyFor(sessionID, toolName, action string, args json.RawMessage) PermissionKey {
	path := ""
	grantKey := string(canonicalArgs(args))
	if isShellTool(toolName) {
		// Shell grants scope to the workspace directory and narrow to the
		// exact command: approving ls does not approve sudo.
		path = normalizePermissionPath(".", g.workspace())
		if cmd := shellCommandArg(args); cmd != "" {
			grantKey = cmd
		}
	} else if raw := argsPath(args); raw != "" {
		path = normalizePermissionPath(raw, g.workspace())
	}
	return PermissionKey{
		SessionID: sessionID,
		ToolName:  toolName,
		Action:    action,
		Path:      path,
		GrantKey:  grantKey,
	}
}

func (g *ApprovalGate) hasGrant(key PermissionKey) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.grants[key]
}

func (g *ApprovalGate) addGrant(key PermissionKey) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grants[key] = true
}

// classify combines the configurable danger list with the pure classifier.
func (g *ApprovalGate) classify(toolName string, args json.RawMessage) (bool, string) {
	g.mu.RLock()
	listed := g.dangerTools[toolName]
	root := g.workspaceRoot
	g.mu.RUnlock()
	if listed {
		return true, "tool is on the configured danger list"
	}
	return ClassifyDanger(toolName, args, root)
}

// --- scope serialization ---

// scopeFor maps a session to its prompt queue. The default (nil SessionScope)
// is ONE global scope: a single prompt at a time app-wide, so parallel tool
// calls queue instead of opening two dialogs.
func (g *ApprovalGate) scopeFor(ctx context.Context, sessionID string) string {
	if g.SessionScope != nil {
		return g.SessionScope(ctx, sessionID)
	}
	return ""
}

// lockScope acquires the scope's single prompt slot, waiting (or cancelling)
// while another prompt holds it. The token channel makes the wait ctx-aware
// so a cancelled turn never hangs behind an open dialog.
func (g *ApprovalGate) lockScope(ctx context.Context, scope string) error {
	g.mu.Lock()
	ch := g.scopes[scope]
	if ch == nil {
		ch = make(chan struct{}, 1)
		g.scopes[scope] = ch
	}
	g.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *ApprovalGate) unlockScope(scope string) {
	g.mu.RLock()
	ch := g.scopes[scope]
	g.mu.RUnlock()
	if ch != nil {
		<-ch
	}
}

// --- first-answer-wins resolution ---

func (g *ApprovalGate) registerPending(p *pendingRequest) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pending[p.id] = p
}

func (g *ApprovalGate) deletePending(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending, id)
}

// Resolve answers a pending request by id (the only thing a responder ever
// echoes). The FIRST terminal answer wins; later answers are no-ops returning
// false. Resolution uses the server-stored request parameters, so a dishonest
// responder cannot widen the grant. Edit is not a terminal answer and is
// rejected here (the edit pass is gate-internal, driven by the prompt hook).
func (g *ApprovalGate) Resolve(id string, choice ApprovalChoice) bool {
	if choice == ApprovalEdit {
		return false
	}
	g.mu.RLock()
	p := g.pending[id]
	g.mu.RUnlock()
	if p == nil {
		return false
	}
	return p.resolve(choice, nil, false)
}

// PendingRequestIDs lists the ids of currently open prompts (inspection/tests).
func (g *ApprovalGate) PendingRequestIDs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ids := make([]string, 0, len(g.pending))
	for id := range g.pending {
		ids = append(ids, id)
	}
	return ids
}

// --- bus + observer plumbing (nil-safe, async tap) ---

func (g *ApprovalGate) publishRequest(ctx context.Context, req pubsub.PermissionRequest) {
	if g.Bus != nil {
		g.Bus.PublishPermissionRequest(ctx, req)
	}
}

func (g *ApprovalGate) publishNotification(ctx context.Context, n pubsub.PermissionNotification) {
	if g.Bus != nil {
		g.Bus.PublishPermissionNotification(ctx, n)
	}
}

func (g *ApprovalGate) publishModeChanged(ctx context.Context, change pubsub.PermissionModeChanged) {
	if g.Bus != nil {
		g.Bus.PublishPermissionModeChanged(ctx, change)
	}
}

// fireShown runs PromptShown off the requesting goroutine. A slow or panicking
// observer can neither delay nor kill the tool.
func (g *ApprovalGate) fireShown(req pubsub.PermissionRequest) {
	obs := g.Observer
	if obs == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		obs.PromptShown(req)
	}()
}

func (g *ApprovalGate) fireResolved(req pubsub.PermissionRequest, outcome PromptOutcome) {
	obs := g.Observer
	if obs == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		obs.PromptResolved(req, outcome)
	}()
}

// grantedNotification publishes the Granted lifecycle event for auto-grants
// that skip the prompt (session auto-approve, keyed grant hit) —
func (g *ApprovalGate) grantedNotification(toolName string) {
	g.publishNotification(context.Background(), pubsub.PermissionNotification{
		ID:      newPermissionID(),
		Tool:    toolName,
		Granted: true,
	})
}
