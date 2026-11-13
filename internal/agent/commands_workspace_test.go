package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
)

// /new must record the workspace, or the next launch would not auto-resume it.
func TestNewCommandRecordsWorkspace(t *testing.T) {
	store, err := session.NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open session store: %v", err)
	}
	defer store.Close()

	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "mock", models: []string{"m1"}})

	workspace := t.TempDir()
	sess := NewSessionWithStore("", "mock", "m1", store)
	cmdCtx := CommandContext{
		Session:       sess,
		Registry:      reg,
		WorkspaceRoot: workspace,
	}

	res := HandleCommand(context.Background(), cmdCtx, "/new")
	if !res.Handled {
		t.Fatalf("/new must be handled, got %+v", res)
	}
	id := sess.ID()
	if id == "" {
		t.Fatal("/new must assign a session ID")
	}

	rec, err := store.GetLastSessionForWorkspace(workspace)
	if err != nil {
		t.Fatalf("failed to query workspace sessions: %v", err)
	}
	if rec == nil || rec.ID != id {
		t.Fatalf("new session %q must be bound to the workspace, got %+v", id, rec)
	}
}
