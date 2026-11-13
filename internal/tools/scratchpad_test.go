package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestScratchpadStoreSetGetAppend(t *testing.T) {
	store := NewScratchpadStore()
	if got := store.Get(); got != "" {
		t.Fatalf("new store should be empty, got %q", got)
	}
	if got := store.Render(); got != "" {
		t.Fatalf("empty store should render empty, got %q", got)
	}

	if err := store.Set("Goal: finish C1"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if got := store.Get(); got != "Goal: finish C1" {
		t.Fatalf("Get = %q", got)
	}

	if err := store.Append("Open file: context.go"); err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	want := "Goal: finish C1\nOpen file: context.go"
	if got := store.Get(); got != want {
		t.Fatalf("Append result = %q, want %q", got, want)
	}

	// Append to an empty store must not introduce a leading newline.
	other := NewScratchpadStore()
	if err := other.Append("first"); err != nil {
		t.Fatalf("Append to empty store failed: %v", err)
	}
	if got := other.Get(); got != "first" {
		t.Fatalf("Append to empty store = %q", got)
	}

	if err := store.Set("  spaced  "); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if got := store.Render(); got != "spaced" {
		t.Fatalf("Render should trim, got %q", got)
	}
}

func TestScratchpadStoreSizeLimit(t *testing.T) {
	store := NewScratchpadStore()
	tooBig := strings.Repeat("x", MaxScratchpadBytes+1)
	if err := store.Set(tooBig); err == nil {
		t.Fatal("Set must reject content over the limit")
	}
	if got := store.Get(); got != "" {
		t.Fatalf("rejected Set must not change the store, got %d bytes", len(got))
	}

	max := strings.Repeat("y", MaxScratchpadBytes)
	if err := store.Set(max); err != nil {
		t.Fatalf("content at the limit must be accepted: %v", err)
	}

	// Append must account for existing content, not just the new fragment.
	if err := store.Append("z"); err == nil {
		t.Fatal("Append must reject a combined size over the limit")
	}
	if got := store.Get(); len(got) != MaxScratchpadBytes {
		t.Fatalf("rejected Append changed the store: %d bytes", len(got))
	}
}

func TestScratchpadToolSpecAndApproval(t *testing.T) {
	tool := &ScratchpadTool{Store: NewScratchpadStore()}
	spec := tool.Spec()
	if spec.Name != "scratchpad" {
		t.Errorf("tool name = %q, want scratchpad", spec.Name)
	}
	if tool.RequiresApproval() {
		t.Error("scratchpad only records text and must not require approval")
	}
	if !strings.Contains(spec.Description, "compaction") {
		t.Errorf("description should explain compaction survival: %s", spec.Description)
	}
}

func TestScratchpadToolReplaceAndAppend(t *testing.T) {
	store := NewScratchpadStore()
	tool := &ScratchpadTool{Store: store}
	ctx := context.Background()

	res, err := tool.Execute(ctx, json.RawMessage(`{"content":"goal A"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("replace failed: err=%v result=%+v", err, res)
	}
	if got := store.Get(); got != "goal A" {
		t.Fatalf("replace result = %q", got)
	}

	res, err = tool.Execute(ctx, json.RawMessage(`{"content":"decision B","mode":"append"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("append failed: err=%v result=%+v", err, res)
	}
	if got := store.Get(); got != "goal A\ndecision B" {
		t.Fatalf("append result = %q", got)
	}

	// Empty replace is an explicit clear.
	res, err = tool.Execute(ctx, json.RawMessage(`{"content":"","mode":"replace"}`))
	if err != nil || res.Error != "" {
		t.Fatalf("clear failed: err=%v result=%+v", err, res)
	}
	if got := store.Get(); got != "" {
		t.Fatalf("clear did not empty the store: %q", got)
	}

	// Empty append is a no-op, not an error.
	if res, err = tool.Execute(ctx, json.RawMessage(`{"content":"","mode":"append"}`)); err != nil || res.Error != "" {
		t.Fatalf("empty append should succeed: err=%v result=%+v", err, res)
	}
}

func TestScratchpadToolRejectsInvalidInput(t *testing.T) {
	tool := &ScratchpadTool{Store: NewScratchpadStore()}
	ctx := context.Background()

	cases := []struct {
		name string
		args string
	}{
		{"invalid json", `{`},
		{"missing content", `{"mode":"replace"}`},
		{"unknown mode", `{"content":"x","mode":"prepend"}`},
		{"over limit", `{"content":"` + strings.Repeat("x", MaxScratchpadBytes+1) + `"}`},
	}
	for _, tc := range cases {
		res, err := tool.Execute(ctx, json.RawMessage(tc.args))
		if err != nil {
			t.Fatalf("%s: unexpected Go error: %v", tc.name, err)
		}
		if res.Error == "" {
			t.Fatalf("%s: expected a tool error result", tc.name)
		}
	}

	unconfigured := &ScratchpadTool{}
	if res, err := unconfigured.Execute(ctx, json.RawMessage(`{"content":"x"}`)); err != nil || res.Error == "" {
		t.Fatalf("missing store must return a tool error, got err=%v result=%+v", err, res)
	}
}
