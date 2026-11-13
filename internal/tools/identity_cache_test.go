package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mevarx/GoCode/internal/ignore"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSensitiveIdentitySetCachedWalksOnce(t *testing.T) {
	ws := t.TempDir()
	secret := filepath.Join(ws, ".env")
	writeTestFile(t, secret, "API_KEY=leak")
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	start := sensitiveIdentityWalks.Load()
	first := SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 1 {
		t.Fatalf("first call walked %d times, want 1", got)
	}
	second := SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 1 {
		t.Fatalf("second call re-walked, total %d walks, want 1", got)
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty identity sets, got %d and %d", len(first), len(second))
	}
	info, err := os.Stat(secret)
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if !IsBlockedByIdentity(first, info) {
		t.Fatal("cached set does not contain the sensitive file identity")
	}
}

func TestSensitiveIdentitySetInvalidationForcesRewalk(t *testing.T) {
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".env"), "API_KEY=leak")
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	start := sensitiveIdentityWalks.Load()
	SensitiveIdentitySet(ws, m)
	SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 1 {
		t.Fatalf("warm cache walked %d times, want 1", got)
	}

	InvalidateSensitiveIdentityCache(ws)
	SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 2 {
		t.Fatalf("after invalidation walked %d times total, want 2", got)
	}
}

func TestSensitiveIdentitySetDifferentRootsAreSeparate(t *testing.T) {
	wsA := t.TempDir()
	wsB := t.TempDir()
	writeTestFile(t, filepath.Join(wsA, ".env"), "A=1")
	writeTestFile(t, filepath.Join(wsB, ".env"), "B=2")
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() {
		InvalidateSensitiveIdentityCache(wsA)
		InvalidateSensitiveIdentityCache(wsB)
	})

	start := sensitiveIdentityWalks.Load()
	SensitiveIdentitySet(wsA, m)
	SensitiveIdentitySet(wsA, m)
	SensitiveIdentitySet(wsB, m)
	SensitiveIdentitySet(wsB, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 2 {
		t.Fatalf("two roots walked %d times, want 2", got)
	}

	// Invalidating one root must not evict the other.
	InvalidateSensitiveIdentityCache(wsA)
	SensitiveIdentitySet(wsA, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 3 {
		t.Fatalf("wsA re-walk missing, total %d walks, want 3", got)
	}
	SensitiveIdentitySet(wsB, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 3 {
		t.Fatalf("wsB should stay cached, total %d walks, want 3", got)
	}
}

func TestSensitiveIdentitySetDetectsExternalHardlink(t *testing.T) {
	oldHome := userHomeDir
	home := t.TempDir()
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = oldHome })

	external := filepath.Join(home, ".ssh", "id_rsa")
	writeTestFile(t, external, "PRIVATE KEY")

	ws := t.TempDir()
	alias := filepath.Join(ws, "notes.txt")
	if err := os.Link(external, alias); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}
	plain := filepath.Join(ws, "plain.txt")
	writeTestFile(t, plain, "ordinary content")

	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	set := SensitiveIdentitySet(ws, m)
	aliasInfo, err := os.Stat(alias)
	if err != nil {
		t.Fatalf("stat alias: %v", err)
	}
	if !IsBlockedByIdentity(set, aliasInfo) {
		t.Fatal("hardlink to external sensitive file was not detected")
	}
	plainInfo, err := os.Stat(plain)
	if err != nil {
		t.Fatalf("stat plain: %v", err)
	}
	if IsBlockedByIdentity(set, plainInfo) {
		t.Fatal("ordinary in-workspace file was incorrectly blocked")
	}
}

func TestFileWriteSuccessInvalidatesIdentityCache(t *testing.T) {
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".env"), "API_KEY=leak")
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	SensitiveIdentitySet(ws, m) // warm
	start := sensitiveIdentityWalks.Load()

	w := &FileWriteTool{WorkspaceRoot: ws, SensitiveMatcher: m}
	args, _ := json.Marshal(fileWriteArgs{Path: "new.txt", Content: "hello"})
	res, err := w.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("write failed: err=%v result=%q", err, res.Error)
	}
	if got := sensitiveIdentityWalks.Load() - start; got != 0 {
		t.Fatalf("execute walked %d times, want 0", got)
	}

	SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 1 {
		t.Fatalf("successful write did not invalidate cache: walked %d times, want 1", got)
	}
}

func TestFileWriteFailureDoesNotInvalidateIdentityCache(t *testing.T) {
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".env"), "API_KEY=leak")
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	SensitiveIdentitySet(ws, m) // warm
	start := sensitiveIdentityWalks.Load()

	w := &FileWriteTool{WorkspaceRoot: ws, SensitiveMatcher: m}
	args, _ := json.Marshal(fileWriteArgs{Path: ".env", Content: "overwritten"})
	res, err := w.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected refused write to a sensitive file")
	}

	SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 0 {
		t.Fatalf("failed write invalidated cache: walked %d times, want 0", got)
	}
}

func TestFileWriteDiskFailureDoesNotInvalidateIdentityCache(t *testing.T) {
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".env"), "API_KEY=leak")
	if err := os.Mkdir(filepath.Join(ws, "adir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	SensitiveIdentitySet(ws, m) // warm
	start := sensitiveIdentityWalks.Load()

	w := &FileWriteTool{WorkspaceRoot: ws, SensitiveMatcher: m}
	args, _ := json.Marshal(fileWriteArgs{Path: "adir", Content: "nope"})
	res, err := w.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected write to a directory to fail")
	}

	SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 0 {
		t.Fatalf("failed disk write invalidated cache: walked %d times, want 0", got)
	}
}

func TestFileWritePreviewDoesNotInvalidateIdentityCache(t *testing.T) {
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".env"), "API_KEY=leak")
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	SensitiveIdentitySet(ws, m) // warm
	start := sensitiveIdentityWalks.Load()

	w := &FileWriteTool{WorkspaceRoot: ws, SensitiveMatcher: m}
	args, _ := json.Marshal(fileWriteArgs{Path: "new.txt", Content: "hello"})
	if _, err := w.Preview(context.Background(), args); err != nil {
		t.Fatalf("preview failed: %v", err)
	}

	SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 0 {
		t.Fatalf("preview invalidated cache: walked %d times, want 0", got)
	}
}

func TestFilePatchSuccessInvalidatesIdentityCache(t *testing.T) {
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".env"), "API_KEY=leak")
	writeTestFile(t, filepath.Join(ws, "code.txt"), "foo bar")
	m := ignore.NewSensitiveMatcher(nil, nil)
	t.Cleanup(func() { InvalidateSensitiveIdentityCache(ws) })

	SensitiveIdentitySet(ws, m) // warm
	start := sensitiveIdentityWalks.Load()

	p := &FilePatchTool{WorkspaceRoot: ws, SensitiveMatcher: m}
	args, _ := json.Marshal(filePatchArgs{Path: "code.txt", Find: "foo", Replace: "baz"})
	res, err := p.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("patch failed: err=%v result=%q", err, res.Error)
	}

	SensitiveIdentitySet(ws, m)
	if got := sensitiveIdentityWalks.Load() - start; got != 1 {
		t.Fatalf("successful patch did not invalidate cache: walked %d times, want 1", got)
	}
}
