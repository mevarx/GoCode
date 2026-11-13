package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mevarx/GoCode/internal/ignore"
)

type FileReadTool struct {
	IgnoreMatcher    ignore.Matcher
	SensitiveMatcher *ignore.SensitiveMatcher
	WorkspaceRoot    string
	// MaxFileBytes bounds whole-file reads; 0 uses defaultMaxFileBytes. Ranged
	// reads (offset/limit) stream instead of rejecting an oversized file.
	MaxFileBytes int64
}

type fileReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"` // 1-based line offset
	Limit  int    `json:"limit,omitempty"`  // maximum lines to return
}

func (f *FileReadTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "file_read",
		Description: "Read the contents of a file or list a directory. Returns file content as text with metadata. Supports line-based offset and limit for large files; files over the size limit must be read with offset and limit.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["path"],
			"properties": {
				"path": {
					"type": "string",
					"description": "Absolute or relative path to the file to read (relative to workspace root)"
				},
				"offset": {
					"type": "integer",
					"description": "1-based starting line number (default: 1)"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of lines to return (default: all)"
				}
			}
		}`),
	}
}

func (f *FileReadTool) RequiresApproval() bool {
	return false
}

func (f *FileReadTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var a fileReadArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}

	if a.Path == "" {
		return Result{Error: "path cannot be empty"}, nil
	}

	var path string
	if f.WorkspaceRoot != "" {
		validatedPath, err := ValidatePath(f.WorkspaceRoot, a.Path)
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		path = validatedPath
	} else {
		path = filepath.Clean(a.Path)
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Error: fmt.Sprintf("file not found: %s", path)}, nil
		}
		return Result{Error: fmt.Sprintf("cannot access file: %v", err)}, nil
	}

	if f.SensitiveMatcher != nil {
		blocked, reason, err := CheckSensitiveFile(f.SensitiveMatcher, path, info.IsDir())
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		if blocked {
			return Result{Error: fmt.Sprintf("%s: %s", path, reason)}, nil
		}
		// SECURITY: hardlink aliases share inodes with sensitive files.
		if !info.IsDir() && f.WorkspaceRoot != "" {
			if IsBlockedByIdentity(SensitiveIdentitySet(f.WorkspaceRoot, f.SensitiveMatcher), info) {
				return Result{Error: fmt.Sprintf("%s: access denied: matches a sensitive file (hardlink alias)", path)}, nil
			}
		}
	} else if f.IgnoreMatcher != nil && f.IgnoreMatcher.IsIgnored(path, info.IsDir()) {
		return Result{Error: fmt.Sprintf("file %s is ignored by ignore rules (.gocodeignore/.gitignore)", path)}, nil
	}

	if info.IsDir() {
		return f.readDirectory(path)
	}

	return f.readFile(path, info, a.Offset, a.Limit)
}

func (f *FileReadTool) readDirectory(path string) (Result, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot read directory: %v", err)}, nil
	}

	var listing []string
	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())

		if f.SensitiveMatcher != nil {
			if blocked, _, err := CheckSensitiveFile(f.SensitiveMatcher, entryPath, entry.IsDir()); blocked || err != nil {
				continue
			}
		} else if f.IgnoreMatcher != nil && f.IgnoreMatcher.IsIgnored(entryPath, entry.IsDir()) {
			continue
		}

		prefix := "📄"
		if entry.IsDir() {
			prefix = "📁"
		}
		listing = append(listing, fmt.Sprintf("%s %s", prefix, entry.Name()))
	}
	return Result{Output: fmt.Sprintf("Directory listing for %s:\n%s", path, joinLines(listing))}, nil
}

const (
	// maxReadBytes bounds the content returned by one file_read call.
	maxReadBytes = 100 * 1024
	// defaultMaxFileBytes bounds a whole-file read; ranged reads stream instead.
	defaultMaxFileBytes int64 = 10 << 20
	// lineBufferBytes bounds the read buffer used while streaming lines.
	lineBufferBytes = 64 * 1024
)

func (f *FileReadTool) maxFileBytes() int64 {
	if f.MaxFileBytes > 0 {
		return f.MaxFileBytes
	}
	return defaultMaxFileBytes
}

func (f *FileReadTool) readFile(path string, info os.FileInfo, offset, limit int) (Result, error) {
	maxBytes := f.maxFileBytes()
	hasRange := offset > 0 || limit > 0
	if info.Size() > maxBytes && !hasRange {
		return tooLargeFile(path, info.Size(), maxBytes), nil
	}

	file, err := os.Open(path)
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot read file: %v", err)}, nil
	}
	defer file.Close()

	if opened, statErr := file.Stat(); statErr == nil {
		// Re-check against the opened handle: the file may have grown since os.Stat.
		if opened.Size() > maxBytes && !hasRange {
			return tooLargeFile(path, opened.Size(), maxBytes), nil
		}
	}

	reader := bufio.NewReaderSize(file, lineBufferBytes)
	totalLines, totalBytes, err := countLines(reader)
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot read file: %v", err)}, nil
	}

	// Guard bounds before slicing: a huge or negative limit can overflow endLine and panic.
	startLine := 1
	truncated := false

	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if offset > 0 {
		startLine = offset
	}
	if startLine > totalLines {
		startLine = totalLines
	}

	if limit > totalLines {
		limit = totalLines
	}
	endLine := totalLines
	if limit > 0 {
		endLine = startLine + limit - 1
		if endLine > totalLines {
			endLine = totalLines
		}
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Result{Error: fmt.Sprintf("cannot read file: %v", err)}, nil
	}
	reader.Reset(file)

	// Selected content is streamed and capped: the whole file is never buffered.
	var content []byte
	appendContent := func(p []byte) bool {
		room := maxReadBytes - len(content)
		if len(p) > room {
			content = append(content, p[:room]...)
			truncated = true
			return false
		}
		content = append(content, p...)
		return true
	}

	first := true
	err = forEachLine(reader, func(lineNum int, line []byte, lineTruncated bool) bool {
		if lineNum < startLine {
			return true
		}
		if lineNum > endLine {
			return false
		}
		if !first {
			if len(content) >= maxReadBytes {
				// No room even for the separator: content is capped.
				truncated = true
				return false
			}
			content = append(content, '\n')
		}
		first = false
		if !appendContent(line) {
			return false
		}
		if lineTruncated {
			truncated = true
			return false
		}
		return true
	})
	if err != nil {
		return Result{Error: fmt.Sprintf("cannot read file: %v", err)}, nil
	}

	if endLine < totalLines && limit > 0 {
		truncated = true
	}

	meta := fmt.Sprintf("path: %s | lines: %d-%d of %d | bytes: %d",
		path, startLine, endLine, totalLines, totalBytes)
	if truncated {
		meta += " | truncated: true"
	}

	output := fmt.Sprintf("[%s]\n%s", meta, string(content))
	return Result{Output: output}, nil
}

func tooLargeFile(path string, size, maxBytes int64) Result {
	return Result{Error: fmt.Sprintf(
		"%s: file too large: %d bytes exceeds %d; use offset and limit to read a range", path, size, maxBytes)}
}

// countLines counts lines with strings.Split semantics: there is always one line
// per newline plus a final (possibly empty) line, so an empty file is one line.
// It also reports bytes consumed. Reads use fixed-size chunks so memory stays
// bounded regardless of file size.
func countLines(r io.Reader) (int, int64, error) {
	buf := make([]byte, lineBufferBytes)
	lines := 0
	var read int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			read += int64(n)
			lines += bytes.Count(buf[:n], []byte{'\n'})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, err
		}
	}
	return lines + 1, read, nil
}

// forEachLine streams lines from r with strings.Split semantics. Each line's
// content is capped at maxReadBytes so a single huge line cannot exhaust memory;
// truncated reports whether content was dropped. fn returns false to stop early.
func forEachLine(r *bufio.Reader, fn func(lineNum int, line []byte, truncated bool) bool) error {
	line := make([]byte, 0, 256)
	cut := false
	lineNum := 0

	emit := func() bool {
		lineNum++
		ok := fn(lineNum, line, cut)
		line = line[:0]
		cut = false
		return ok
	}

	for {
		chunk, err := r.ReadSlice('\n')
		complete := false
		if len(chunk) > 0 {
			piece := chunk
			complete = piece[len(piece)-1] == '\n'
			if complete {
				piece = piece[:len(piece)-1]
			}
			if !cut {
				room := maxReadBytes - len(line)
				if room > len(piece) {
					room = len(piece)
				}
				line = append(line, piece[:room]...)
				if room < len(piece) {
					cut = true
				}
			}
			if complete && !emit() {
				return nil
			}
		}
		switch err {
		case nil, bufio.ErrBufferFull:
			continue
		case io.EOF:
			// A final line without a trailing newline, or the trailing empty line
			// produced by a file that ends with '\n' (empty files yield one line).
			if !complete {
				emit()
			}
			return nil
		default:
			return err
		}
	}
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
