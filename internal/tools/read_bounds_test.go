package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSparseLeadFile creates a file that starts with lead and is then extended
// to size, modelling a huge log whose first lines are the only readable ones.
func writeSparseLeadFile(t *testing.T, path, lead string, size int64) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if _, err := f.WriteString(lead); err != nil {
		f.Close()
		t.Fatalf("write %s: %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("truncate %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func TestFileReadTool_RejectsOversizedFileWithoutRange(t *testing.T) {
	dir := t.TempDir()
	writeSparseLeadFile(t, filepath.Join(dir, "huge.log"), "first line\n", 11<<20)

	tool := &FileReadTool{WorkspaceRoot: dir}
	args, err := json.Marshal(fileReadArgs{Path: "huge.log"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Output != "" {
		t.Errorf("oversized read must not return content, got %d bytes", len(res.Output))
	}
	if !strings.Contains(res.Error, "file too large") {
		t.Errorf("expected a file-too-large error, got %q", res.Error)
	}
	if !strings.Contains(res.Error, "offset and limit") {
		t.Errorf("error should point at offset/limit, got %q", res.Error)
	}
}

func TestFileReadTool_OversizedFileRangedReadIsBounded(t *testing.T) {
	dir := t.TempDir()
	writeSparseLeadFile(t, filepath.Join(dir, "huge.log"), "first line\n", 11<<20)

	tool := &FileReadTool{WorkspaceRoot: dir}
	args, err := json.Marshal(fileReadArgs{Path: "huge.log", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected result error: %s", res.Error)
	}
	meta, selected, _ := strings.Cut(res.Output, "\n")
	if !strings.Contains(selected, "first line") {
		t.Errorf("expected the first line, got %d bytes of selected content", len(selected))
	}
	// "first line\n" plus one unterminated 11 MiB line of padding.
	if !strings.Contains(meta, "lines: 1-2 of 2") {
		t.Errorf("expected line metadata to report 2 lines, got %q", meta)
	}
	if !strings.Contains(meta, "truncated: true") {
		t.Errorf("expected a truncation notice, got %q", meta)
	}
	if len(selected) > maxReadBytes {
		t.Errorf("selected content is not bounded: %d bytes", len(selected))
	}
	if len(res.Output) > maxReadBytes+1024 {
		t.Errorf("ranged output is not bounded: %d bytes", len(res.Output))
	}
}

func TestFileReadTool_MaxFileBytesOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &FileReadTool{WorkspaceRoot: dir, MaxFileBytes: 4}
	args, err := json.Marshal(fileReadArgs{Path: "small.txt"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !strings.Contains(res.Error, "file too large: 6 bytes exceeds 4") {
		t.Errorf("expected the configured cap in the error, got %q", res.Error)
	}

	args, err = json.Marshal(fileReadArgs{Path: "small.txt", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	res, err = tool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("ranged read should succeed, got %v / %q", err, res.Error)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Errorf("expected the file content, got %q", res.Output)
	}
}

func TestCodeSearchTool_SkipsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	writeSparseLeadFile(t, filepath.Join(dir, "big.txt"), "needle here\n", 5<<20)
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, err := json.Marshal(codeSearchArgs{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}

	tool := &CodeSearchTool{WorkspaceRoot: dir}
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected result error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "big.txt: skipped (file too large: 5242880 bytes > 4194304)") {
		t.Errorf("expected the oversized file to be skipped, got:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "big.txt:1:") {
		t.Errorf("oversized file must not contribute matches, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "small.txt:1: needle here") {
		t.Errorf("small files must still match, got:\n%s", res.Output)
	}

	// The configured cap must override the default.
	limited := &CodeSearchTool{WorkspaceRoot: dir, MaxFileBytes: 8}
	res, err = limited.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v / %q", err, res.Error)
	}
	if !strings.Contains(res.Output, "small.txt: skipped (file too large: 12 bytes > 8)") {
		t.Errorf("configured cap was not applied, got:\n%s", res.Output)
	}
}

func TestCodeSearchTool_ScanLineCap(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &CodeSearchTool{WorkspaceRoot: dir, MaxScanLines: 10}

	args, err := json.Marshal(codeSearchArgs{Query: "line 25"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v / %q", err, res.Error)
	}
	if strings.Contains(res.Output, "line 25") {
		t.Errorf("matches past the scan cap must not be reported, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "skipped (line cap") {
		t.Errorf("expected a scan-cap notice, got:\n%s", res.Output)
	}

	args, err = json.Marshal(codeSearchArgs{Query: "line 3"})
	if err != nil {
		t.Fatal(err)
	}
	res, err = tool.Execute(context.Background(), args)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v / %q", err, res.Error)
	}
	if !strings.Contains(res.Output, "many.txt:3: line 3") {
		t.Errorf("matches inside the scan cap must be reported, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "skipped (line cap") {
		t.Errorf("expected the scan to stay capped, got:\n%s", res.Output)
	}
}
