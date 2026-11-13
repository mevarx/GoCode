package agent

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

// TestStdinArgEditorReadsOneJSONObject pins the plain loop's "e" plumbing: the
// editor consumes exactly one line from the shared reader and rejects
// anything that is not valid JSON.
func TestStdinArgEditorReadsOneJSONObject(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("{\"command\":\"echo edited\"}\nnext-answer\n"))
	edit := stdinArgEditor(reader)

	edited, err := edit("shell_exec", json.RawMessage(`{"command":"echo original"}`))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if string(edited) != `{"command":"echo edited"}` {
		t.Fatalf("edited args = %s", edited)
	}

	// The next line is still available for the re-prompt / next answer.
	rest, _ := reader.ReadString('\n')
	if strings.TrimSpace(rest) != "next-answer" {
		t.Fatalf("editor consumed more than one line: %q", rest)
	}
}

func TestStdinArgEditorRejectsInvalidJSON(t *testing.T) {
	edit := stdinArgEditor(bufio.NewReader(strings.NewReader("not json\n")))
	if _, err := edit("shell_exec", nil); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestStdinArgEditorReportsEOF(t *testing.T) {
	edit := stdinArgEditor(bufio.NewReader(strings.NewReader("")))
	if _, err := edit("shell_exec", nil); err == nil {
		t.Fatal("EOF was not reported")
	}
}
