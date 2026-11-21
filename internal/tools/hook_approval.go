package tools

import "context"

// Per-call hook approval. A PreToolUse
// hook that answers "allow" stamps the context for ONE tool call; the gate
// consumes the stamp in its decision order AFTER the config auto_approve
// allowlist and BEFORE keyed session grants, short-circuiting the prompt for
// that call only and publishing a Granted notification for audit.
//
// Security invariants: the deny-list, yolo's dangerous-call refusal and the
// config allowlist are all checked BEFORE the stamp, so a hook allow can
// never bypass them; the stamp is keyed by toolCallID, consumed exactly once,
// and cleared at every turn boundary, so it can never approve a second call
// or outlive its turn.

// hookApprovalKey is the context key carrying the per-call approval stamp.
type hookApprovalKey struct{}

// WithHookApproval stamps ctx with a hook approval for toolCallID. The gate
// honors it only when the same call id was registered via GrantHookApproval
// and the call being prepared carries that exact id.
func WithHookApproval(ctx context.Context, toolCallID string) context.Context {
	if toolCallID == "" {
		return ctx
	}
	return context.WithValue(ctx, hookApprovalKey{}, toolCallID)
}

// hookApprovalFromContext reads the stamp, or "" when absent.
func hookApprovalFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(hookApprovalKey{}).(string); ok {
		return v
	}
	return ""
}

// GrantHookApproval registers a one-shot hook approval for toolCallID. The
// loop calls it when a PreToolUse aggregation answers allow; the matching
// PrepareWithID consumes it. An empty id is ignored.
func (g *ApprovalGate) GrantHookApproval(toolCallID string) {
	if toolCallID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hookApprovals == nil {
		g.hookApprovals = make(map[string]bool)
	}
	g.hookApprovals[toolCallID] = true
}

// consumeHookApproval atomically takes the registration for toolCallID. The
// first consumer wins; a replayed stamp finds nothing.
func (g *ApprovalGate) consumeHookApproval(toolCallID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.hookApprovals[toolCallID] {
		return false
	}
	delete(g.hookApprovals, toolCallID)
	return true
}

// hookApprovalCount reports pending registrations (inspection/tests).
func (g *ApprovalGate) hookApprovalCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.hookApprovals)
}
