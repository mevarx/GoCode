package main

import (
	"path/filepath"
	"testing"

	"github.com/mevarx/GoCode/internal/session"
)

func newResumeStore(t *testing.T) *session.SessionStore {
	t.Helper()
	store, err := session.NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open session store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// Auto-resume must never cross workspaces: repo B starts fresh even when repo A
// has a recent transcript.
func TestSelectSessionForResumeScopesAutoResume(t *testing.T) {
	store := newResumeStore(t)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	if _, err := store.CreateSessionWithWorkspace("sess-a", "Repo A", "openai", "gpt-4o", workspaceA); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	got, err := selectSessionForResume(store, "", workspaceA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ID != "sess-a" {
		t.Fatalf("expected sess-a for workspace A, got %+v", got)
	}

	got, err = selectSessionForResume(store, "", workspaceB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("workspace B must start fresh, resumed %q", got.ID)
	}
}

// An explicit --session ID is user intent and wins over the workspace check.
func TestSelectSessionForResumeExplicitIDWins(t *testing.T) {
	store := newResumeStore(t)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	if _, err := store.CreateSessionWithWorkspace("sess-a", "Repo A", "openai", "gpt-4o", workspaceA); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	got, err := selectSessionForResume(store, "sess-a", workspaceB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.ID != "sess-a" {
		t.Fatalf("explicit ID must be loaded even from another workspace, got %+v", got)
	}

	got, err = selectSessionForResume(store, "missing", workspaceB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("unknown explicit ID must yield a fresh session, got %+v", got)
	}
}

func TestSelectSessionForResumeWithoutStore(t *testing.T) {
	got, err := selectSessionForResume(nil, "sess-a", t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("no store must yield no session, got %+v", got)
	}
}
