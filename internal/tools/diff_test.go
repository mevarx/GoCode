package tools

import (
	"strings"
	"testing"
)

// countDiffLines returns the number of added and removed content lines,
// ignoring the ---/+++ file headers.
func countDiffLines(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func TestGenerateUnifiedDiff_InsertTop(t *testing.T) {
	oldContent := "alpha\nbeta\ngamma\n"
	newContent := "inserted\nalpha\nbeta\ngamma\n"

	diff := generateUnifiedDiff("file.txt", oldContent, newContent)

	if !strings.Contains(diff, "@@ -1,3 +1,4 @@") {
		t.Errorf("expected hunk header @@ -1,3 +1,4 @@, got:\n%s", diff)
	}
	if added, removed := countDiffLines(diff); added != 1 || removed != 0 {
		t.Errorf("expected exactly 1 addition and 0 removals, got +%d/-%d:\n%s", added, removed, diff)
	}
	if strings.Count(diff, "@@") != 2 { // one header opens and closes with @@
		t.Errorf("expected a single hunk, got:\n%s", diff)
	}
}

func TestGenerateUnifiedDiff_PureDeletion(t *testing.T) {
	oldContent := "alpha\nbeta\ngamma\n"
	newContent := "alpha\ngamma\n"

	diff := generateUnifiedDiff("file.txt", oldContent, newContent)

	if !strings.Contains(diff, "@@ -1,3 +1,2 @@") {
		t.Errorf("expected hunk header @@ -1,3 +1,2 @@, got:\n%s", diff)
	}
	if added, removed := countDiffLines(diff); added != 0 || removed != 1 {
		t.Errorf("expected exactly 0 additions and 1 removal, got +%d/-%d:\n%s", added, removed, diff)
	}
}

func TestGenerateUnifiedDiff_AppendEnd(t *testing.T) {
	oldContent := "alpha\nbeta\ngamma\n"
	newContent := "alpha\nbeta\ngamma\ndelta\n"

	diff := generateUnifiedDiff("file.txt", oldContent, newContent)

	if !strings.Contains(diff, "@@ -1,3 +1,4 @@") {
		t.Errorf("expected hunk header @@ -1,3 +1,4 @@, got:\n%s", diff)
	}
	if added, removed := countDiffLines(diff); added != 1 || removed != 0 {
		t.Errorf("expected exactly 1 addition and 0 removals, got +%d/-%d:\n%s", added, removed, diff)
	}
}

func TestGenerateUnifiedDiff_MidFileReplace(t *testing.T) {
	oldContent := "alpha\nbeta\ngamma\n"
	newContent := "alpha\nBETA\ngamma\n"

	diff := generateUnifiedDiff("file.txt", oldContent, newContent)

	if !strings.Contains(diff, "@@ -1,3 +1,3 @@") {
		t.Errorf("expected hunk header @@ -1,3 +1,3 @@, got:\n%s", diff)
	}
	if added, removed := countDiffLines(diff); added != 1 || removed != 1 {
		t.Errorf("expected exactly 1 addition and 1 removal (not a whole-file rewrite), got +%d/-%d:\n%s", added, removed, diff)
	}
}

func TestGenerateUnifiedDiff_EmptyToContent(t *testing.T) {
	diff := generateUnifiedDiff("file.txt", "", "alpha\nbeta\n")

	if !strings.HasPrefix(diff, "--- /dev/null\n") {
		t.Errorf("expected /dev/null source header, got:\n%s", diff)
	}
	if !strings.Contains(diff, "+++ file.txt\n") {
		t.Errorf("expected target header with filename, got:\n%s", diff)
	}
	if !strings.Contains(diff, "@@ -0,0 +1,2 @@") {
		t.Errorf("expected hunk header @@ -0,0 +1,2 @@, got:\n%s", diff)
	}
	if added, removed := countDiffLines(diff); added != 2 || removed != 0 {
		t.Errorf("expected exactly 2 additions and 0 removals, got +%d/-%d:\n%s", added, removed, diff)
	}
}

func TestGenerateUnifiedDiff_ContentToEmpty(t *testing.T) {
	diff := generateUnifiedDiff("file.txt", "alpha\nbeta\ngamma\n", "")

	if !strings.Contains(diff, "@@ -1,3 +0,0 @@") {
		t.Errorf("expected hunk header @@ -1,3 +0,0 @@, got:\n%s", diff)
	}
	if added, removed := countDiffLines(diff); added != 0 || removed != 3 {
		t.Errorf("expected exactly 0 additions and 3 removals, got +%d/-%d:\n%s", added, removed, diff)
	}
}

func TestGenerateUnifiedDiff_NoOp(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"

	diff := generateUnifiedDiff("file.txt", content, content)

	if diff != "" {
		t.Errorf("expected empty diff for identical content, got:\n%s", diff)
	}
	if added, removed := countDiffLines(diff); added != 0 || removed != 0 {
		t.Errorf("expected no +/- content lines, got +%d/-%d:\n%s", added, removed, diff)
	}
}

func TestGenerateUnifiedDiff_CRLFNormalized(t *testing.T) {
	oldContent := "alpha\r\nbeta\r\ngamma\r\n"
	newContent := "alpha\nbeta\ngamma\n"

	if diff := generateUnifiedDiff("file.txt", oldContent, newContent); diff != "" {
		t.Errorf("expected CRLF-only change to produce no diff, got:\n%s", diff)
	}
}
