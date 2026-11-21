package tui

import "time"

// Input grace for the permission dialog: a request
// that arrives while the user is typing must not eat a reflexive keystroke.
// Keys are absorbed until 425ms of quiet passes, capped at 1500ms from open;
// each absorbed key restarts the quiet window but never extends past the cap.
// Reopening the same request within 500ms of its close skips grace entirely.
// Grace only DELAYS key handling — it can never approve anything.
const (
	approvalGraceQuiet      = 425 * time.Millisecond
	approvalGraceCap        = 1500 * time.Millisecond
	approvalGraceReopenSkip = 500 * time.Millisecond
)

// approvalGrace is the per-dialog timing state. It is a pure function of
// timestamps so tests drive it with a fake clock and no timer messages.
type approvalGrace struct {
	active    bool
	openedAt  time.Time
	lastKeyAt time.Time
}

func (g *approvalGrace) open(now time.Time) {
	g.active = true
	g.openedAt = now
	g.lastKeyAt = now
}

func (g *approvalGrace) skip() { g.active = false }

// absorb reports whether a key arriving at now must be swallowed. A key at or
// after the grace end is processed (and ends the grace); a key before it is
// absorbed and restarts the quiet window, bounded by the hard cap.
func (g *approvalGrace) absorb(now time.Time) bool {
	if !g.active {
		return false
	}
	end := g.lastKeyAt.Add(approvalGraceQuiet)
	if capEnd := g.openedAt.Add(approvalGraceCap); end.After(capEnd) {
		end = capEnd
	}
	if !now.Before(end) {
		g.active = false
		return false
	}
	g.lastKeyAt = now
	return true
}

// approvalRequestID identifies a request for the reopen-skip window: the same
// tool with the same arguments is the same decision asked twice.
func approvalRequestID(req ApprovalRequest) string {
	return req.ToolName + "|" + string(req.Args)
}
