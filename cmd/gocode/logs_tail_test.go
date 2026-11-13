package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailFileLinesReturnsLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	var sb strings.Builder
	for i := 1; i <= 10000; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	lines, err := tailFileLines(f, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"line 9998", "line 9999", "line 10000"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestTailFileLinesTailLargerThanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	lines, err := tailFileLines(f, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

func TestTailFileLinesEmptyAndZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if lines, err := tailFileLines(f, 10); err != nil || len(lines) != 0 {
		t.Fatalf("empty file: lines=%v err=%v", lines, err)
	}
	if lines, err := tailFileLines(f, 0); err != nil || lines != nil {
		t.Fatalf("zero tail: lines=%v err=%v", lines, err)
	}
}

func TestTailFileLinesNoTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	lines, err := tailFileLines(f, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "second" || lines[1] != "third" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}
