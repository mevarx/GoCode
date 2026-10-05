package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// guardConfigFor mirrors runAgent wiring so config-to-guard mapping stays testable.
func guardConfigFor(cfg config.Config) agent.LoopGuardConfig {
	return agent.LoopGuardConfig{
		MaxIterations:    cfg.Tools.MaxToolIterations,
		MaxRepeatedCalls: cfg.Tools.MaxRepeatedToolCalls,
	}
}

func TestGuardConfigFor(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MaxToolIterations = 9
	cfg.Tools.MaxRepeatedToolCalls = 2

	got := guardConfigFor(cfg)
	if got.MaxIterations != 9 {
		t.Errorf("expected 9 iterations, got %d", got.MaxIterations)
	}
	if got.MaxRepeatedCalls != 2 {
		t.Errorf("expected 2 repeat calls, got %d", got.MaxRepeatedCalls)
	}
}

// An unset config must fall back to the guard defaults rather than allowing
// unbounded turns.
func TestGuardConfigForUnsetFallsBackToDefaults(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MaxToolIterations = 0
	cfg.Tools.MaxRepeatedToolCalls = 0

	guard := agent.NewLoopGuardWithConfig(guardConfigFor(cfg))
	if guard.MaxIterations != agent.DefaultMaxToolIterations {
		t.Errorf("expected default iterations, got %d", guard.MaxIterations)
	}
	if guard.MaxRepeatedCalls != agent.DefaultMaxRepeatedToolCalls {
		t.Errorf("expected default repeat limit, got %d", guard.MaxRepeatedCalls)
	}
}

// A configured limit must actually bound a turn through the guard the wiring
// produces, not merely be stored on the struct.
func TestConfiguredLimitBoundsTheTurn(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MaxToolIterations = 2
	cfg.Tools.MaxRepeatedToolCalls = 0

	guard := agent.NewLoopGuardWithConfig(guardConfigFor(cfg))

	// Distinct calls so only the iteration cap can stop the loop.
	var stopped bool
	for i := 0; i < 5; i++ {
		err := guard.CheckCall([]provider.ToolCall{{
			ID:   "c",
			Name: "file_read",
			Args: []byte(`{"path":"f` + string(rune('a'+i)) + `"}`),
		}})
		if err != nil {
			stopped = true
			break
		}
	}
	if !stopped {
		t.Fatal("configured iteration cap of 2 did not stop the turn")
	}
}

// persist=false must prevent store creation.
func TestPersistDisabledMeansNoStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	store, err := openSessionStore(false, dbPath)
	if err != nil {
		t.Errorf("persistence off must not be an error, got %v", err)
	}
	if store != nil {
		t.Error("store must not be created when persistence is disabled")
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Error("no database file may be created when persistence is disabled")
	}
}

func TestPersistEnabledCreatesStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	store, err := openSessionStore(true, dbPath)
	if err != nil {
		t.Fatalf("store creation failed: %v", err)
	}
	if store == nil {
		t.Fatal("store must be created when persistence is enabled")
	}
	defer store.Close()

	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("expected database file to exist: %v", statErr)
	}
}

// An unopenable store must be non-fatal: the store stays nil, never half-open.
func TestPersistEnabledToleratesUnwritablePath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	store, err := openSessionStore(true, filepath.Join(file, "sessions.db"))
	if err == nil {
		t.Error("expected an error when the database path is unusable")
	}
	if store != nil {
		t.Error("store must be nil when the database cannot be opened")
	}
}

// Approval permissions must reach the gate with whitespace trimmed, so a
// config entry written as " code_search " does not silently fail to match.
func TestApprovalPermissionsReachTheGate(t *testing.T) {
	gate := tools.NewApprovalGateWithPermissions(
		[]string{"file_read", " code_search "},
		[]string{"shell_exec"},
	)

	if !gate.IsAutoApproved("file_read") {
		t.Error("file_read should be auto-approved")
	}
	if !gate.IsAutoApproved("code_search") {
		t.Error("code_search should be auto-approved after trimming")
	}
	if gate.IsAutoApproved("shell_exec") {
		t.Error("shell_exec is denied and must not be auto-approved")
	}
	if !gate.IsDenied("shell_exec") {
		t.Error("shell_exec must be denied")
	}
}
