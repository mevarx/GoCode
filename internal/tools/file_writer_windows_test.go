//go:build windows

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWriteToolFile_RetryOnSharingViolation_Success(t *testing.T) {
	var attempts int
	var sleeps []time.Duration

	mockWrite := func(path string, data []byte, perm os.FileMode) error {
		attempts++
		if attempts < 3 {
			return windows.ERROR_SHARING_VIOLATION
		}
		return nil
	}

	mockSleep := func(d time.Duration) {
		sleeps = append(sleeps, d)
	}

	err := writeToolFileWithRetry("test.txt", []byte("data"), 0o644, mockWrite, mockSleep)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}

	expectedSleeps := []time.Duration{2 * time.Millisecond, 4 * time.Millisecond}
	if len(sleeps) != len(expectedSleeps) {
		t.Fatalf("expected %d sleeps, got %d: %v", len(expectedSleeps), len(sleeps), sleeps)
	}
	for i, d := range expectedSleeps {
		if sleeps[i] != d {
			t.Errorf("sleep[%d] = %v, expected %v", i, sleeps[i], d)
		}
	}
}

func TestWriteToolFile_RetryOnSharingViolation_Exhaustion(t *testing.T) {
	var attempts int
	var sleeps []time.Duration

	mockWrite := func(path string, data []byte, perm os.FileMode) error {
		attempts++
		return windows.ERROR_SHARING_VIOLATION
	}

	mockSleep := func(d time.Duration) {
		sleeps = append(sleeps, d)
	}

	err := writeToolFileWithRetry("test.txt", []byte("data"), 0o644, mockWrite, mockSleep)
	if err == nil {
		t.Fatal("expected error on exhaustion, got nil")
	}

	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Errorf("expected ERROR_SHARING_VIOLATION, got: %v", err)
	}

	if attempts != 5 {
		t.Errorf("expected 5 attempts, got %d", attempts)
	}

	// 5 attempts means 4 sleeps between them (2ms, 4ms, 8ms, 16ms)
	expectedSleeps := []time.Duration{
		2 * time.Millisecond,
		4 * time.Millisecond,
		8 * time.Millisecond,
		16 * time.Millisecond,
	}
	if len(sleeps) != len(expectedSleeps) {
		t.Fatalf("expected %d sleeps, got %d: %v", len(expectedSleeps), len(sleeps), sleeps)
	}
	for i, d := range expectedSleeps {
		if sleeps[i] != d {
			t.Errorf("sleep[%d] = %v, expected %v", i, sleeps[i], d)
		}
	}
}

func TestWriteToolFile_ImmediateFailureOnUnrelatedError(t *testing.T) {
	testCases := []struct {
		name string
		err  error
	}{
		{"permission denied", os.ErrPermission},
		{"not found", os.ErrNotExist},
		{"access denied", windows.ERROR_ACCESS_DENIED},
		{"disk full", windows.ERROR_DISK_FULL},
		{"custom error", errors.New("something went wrong")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int
			var sleeps []time.Duration

			mockWrite := func(path string, data []byte, perm os.FileMode) error {
				attempts++
				return tc.err
			}

			mockSleep := func(d time.Duration) {
				sleeps = append(sleeps, d)
			}

			err := writeToolFileWithRetry("test.txt", []byte("data"), 0o644, mockWrite, mockSleep)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("expected %v, got %v", tc.err, err)
			}
			if attempts != 1 {
				t.Errorf("expected exactly 1 attempt without retries, got %d", attempts)
			}
			if len(sleeps) != 0 {
				t.Errorf("expected 0 sleeps for unrelated error, got %d", len(sleeps))
			}
		})
	}
}

func TestWriteToolFile_WrappedSharingViolation(t *testing.T) {
	wrapped := &os.PathError{
		Op:   "open",
		Path: `C:\temp\file.txt`,
		Err:  windows.ERROR_SHARING_VIOLATION,
	}

	if !isSharingViolation(wrapped) {
		t.Error("expected wrapped PathError to be recognized as sharing violation")
	}

	doubleWrapped := fmt.Errorf("write failed: %w", wrapped)
	if !isSharingViolation(doubleWrapped) {
		t.Error("expected double wrapped error to be recognized as sharing violation")
	}

	unrelated := &os.PathError{
		Op:   "open",
		Path: `C:\temp\file.txt`,
		Err:  windows.ERROR_ACCESS_DENIED,
	}
	if isSharingViolation(unrelated) {
		t.Error("ACCESS_DENIED should not be recognized as sharing violation")
	}
}

func TestWriteToolFile_RepeatedPatchAndOverwriteWindows(t *testing.T) {
	const iterations = 30
	dir := t.TempDir()

	for i := range iterations {
		testFile := filepath.Join(dir, fmt.Sprintf("repeat_%d.txt", i))

		// 1. Write file via FileWriteTool
		writeTool := &FileWriteTool{}
		wArgs, _ := json.Marshal(fileWriteArgs{Path: testFile, Content: "initial content"})
		wRes, err := writeTool.Execute(context.Background(), wArgs)
		if err != nil {
			t.Fatalf("iteration %d write error: %v", i, err)
		}
		if wRes.Error != "" {
			t.Fatalf("iteration %d write result error: %s", i, wRes.Error)
		}

		// 2. Overwrite file via FileWriteTool (exercises read -> write)
		wArgs2, _ := json.Marshal(fileWriteArgs{Path: testFile, Content: "overwritten content"})
		wRes2, err := writeTool.Execute(context.Background(), wArgs2)
		if err != nil {
			t.Fatalf("iteration %d overwrite error: %v", i, err)
		}
		if wRes2.Error != "" {
			t.Fatalf("iteration %d overwrite result error: %s", i, wRes2.Error)
		}

		// 3. Patch file via FilePatchTool (exercises read -> write)
		patchTool := &FilePatchTool{}
		pArgs, _ := json.Marshal(filePatchArgs{Path: testFile, Find: "overwritten", Replace: "patched"})
		pRes, err := patchTool.Execute(context.Background(), pArgs)
		if err != nil {
			t.Fatalf("iteration %d patch error: %v", i, err)
		}
		if pRes.Error != "" {
			t.Fatalf("iteration %d patch result error: %s", i, pRes.Error)
		}

		data, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("iteration %d read verify error: %v", i, err)
		}
		if string(data) != "patched content" {
			t.Errorf("iteration %d expected 'patched content', got %q", i, string(data))
		}
	}
}

func TestTools_OnModified_FailedWriteDoesNotTrigger(t *testing.T) {
	var modified []string
	onMod := func(p string) {
		modified = append(modified, p)
	}

	invalidPath := "C:\\invalid*dir?\\file.txt"
	writeTool := &FileWriteTool{OnModified: onMod}
	wArgs, _ := json.Marshal(fileWriteArgs{Path: invalidPath, Content: "content"})
	wRes, err := writeTool.Execute(context.Background(), wArgs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if wRes.Error == "" {
		t.Error("expected write error for invalid path")
	}
	if len(modified) != 0 {
		t.Errorf("expected OnModified not to be called on failed write, got %v", modified)
	}

	patchTool := &FilePatchTool{OnModified: onMod}
	pArgs, _ := json.Marshal(filePatchArgs{Path: filepath.Join(t.TempDir(), "nonexistent.txt"), Find: "a", Replace: "b"})
	pRes, err := patchTool.Execute(context.Background(), pArgs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if pRes.Error == "" {
		t.Error("expected patch error for nonexistent file")
	}
	if len(modified) != 0 {
		t.Errorf("expected OnModified not to be called on failed patch, got %v", modified)
	}
}
