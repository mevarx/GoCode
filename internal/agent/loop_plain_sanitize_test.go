package agent

import (
	"io"
	"os"
	"strings"
	"testing"
)

func captureRenderPlain(t *testing.T, events ...LoopEvent) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	a := &AgentLoop{}
	for _, ev := range events {
		a.renderPlain(ev)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(out)
}

func TestRenderPlainSanitizesModelText(t *testing.T) {
	out := captureRenderPlain(t,
		LoopEvent{Kind: EventDelta, Text: "stream\x1b[2Jed"},
		LoopEvent{Kind: EventNotice, Text: "note\x1b[1;1Hified"},
		LoopEvent{Kind: EventExit, Text: "bye\x1b[2J"},
		LoopEvent{Kind: EventError, Text: "boom\x1b]52;c;SGVsbG8=\x07"},
		LoopEvent{Kind: EventToolResult, Tool: "tool_\x1b[2Jname", Text: "out\x1b[2Jput"},
	)

	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("renderPlain output contains ESC: %q", out)
	}
	for _, want := range []string{"streamed", "noteified", "bye", "boom", "output", "tool_name"} {
		if !strings.Contains(out, want) {
			t.Errorf("visible text %q lost from output: %q", want, out)
		}
	}
}
