package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

type ApprovalGate struct {
	autoApprove map[string]bool
	deny        map[string]bool
	mu          sync.RWMutex
	OnPresent   func(toolName string, args json.RawMessage, preview string) (bool, error)

	// inputReader shares the caller's stdin reader; a second scanner would swallow piped answers.
	inputReader *bufio.Reader
	muInput     sync.Mutex
}

// SetInputReader makes the gate read answers from r. Callers sharing the stream must pass their reader.
func (g *ApprovalGate) SetInputReader(r *bufio.Reader) {
	g.muInput.Lock()
	defer g.muInput.Unlock()
	g.inputReader = r
}

func (g *ApprovalGate) readLine() (string, error) {
	g.muInput.Lock()
	r := g.inputReader
	g.muInput.Unlock()

	if r != nil {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return line, nil
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

func NewApprovalGate() *ApprovalGate {
	return &ApprovalGate{
		autoApprove: make(map[string]bool),
		deny:        make(map[string]bool),
	}
}

func NewApprovalGateWithPermissions(autoApprove, deny []string) *ApprovalGate {
	g := NewApprovalGate()
	g.SetPermissions(autoApprove, deny)
	return g
}

func (g *ApprovalGate) SetPermissions(autoApprove, deny []string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.autoApprove = make(map[string]bool, len(autoApprove))
	for _, name := range autoApprove {
		g.autoApprove[strings.TrimSpace(name)] = true
	}

	g.deny = make(map[string]bool, len(deny))
	for _, name := range deny {
		g.deny[strings.TrimSpace(name)] = true
	}
}

func (g *ApprovalGate) IsAutoApproved(toolName string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.autoApprove[toolName]
}

func (g *ApprovalGate) IsDenied(toolName string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.deny[toolName]
}

func (g *ApprovalGate) RequestApproval(toolName string, args json.RawMessage, preview string) (bool, error) {
	if g.IsDenied(toolName) {
		return false, nil
	}
	if g.IsAutoApproved(toolName) {
		return true, nil
	}

	if g.OnPresent != nil {
		return g.OnPresent(toolName, args, preview)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("🔧 Tool: %s\n", toolName)
	fmt.Println(strings.Repeat("─", 50))

	var prettyArgs map[string]interface{}
	if err := json.Unmarshal(args, &prettyArgs); err == nil {
		for k, v := range prettyArgs {
			fmt.Printf("  %s: %v\n", k, v)
		}
	} else {
		fmt.Printf("  args: %s\n", string(args))
	}

	if preview != "" {
		fmt.Println()
		fmt.Println("Preview:")
		fmt.Println(preview)
	}

	fmt.Println(strings.Repeat("─", 50))
	fmt.Print("Approve? [y/N]: ")

	line, err := g.readLine()
	if err != nil {
		return false, fmt.Errorf("failed to read input: %w", err)
	}

	response := strings.TrimSpace(strings.ToLower(line))
	return response == "y" || response == "yes", nil
}

// WrapExecution runs preview → approval → execute. Denied operations never call Execute.
func (g *ApprovalGate) WrapExecution(ctx context.Context, tool Tool, args json.RawMessage) (Result, error) {
	result, err := g.wrapExecution(ctx, tool, args)
	return redactToolResult(result, err)
}

// redactToolResult masks credentials in output, error and diff. WrapExecution is the transcript boundary.
func redactToolResult(r Result, err error) (Result, error) {
	if err != nil {
		return r, err
	}
	var hits []string
	r.Output, hits = RedactToolOutput(r.Output)
	var errHits []string
	r.Error, errHits = RedactToolOutput(r.Error)
	var diffHits []string
	r.Diff, diffHits = RedactToolOutput(r.Diff)
	hits = append(hits, errHits...)
	hits = append(hits, diffHits...)
	if len(hits) > 0 {
		r.Output += DescribeRedactions(hits)
	}
	return r, err
}

func (g *ApprovalGate) wrapExecution(ctx context.Context, tool Tool, args json.RawMessage) (Result, error) {
	toolName := tool.Spec().Name

	if g.IsDenied(toolName) {
		return Result{Error: fmt.Sprintf("execution of tool %q is denied by permissions configuration", toolName)}, nil
	}

	if g.IsAutoApproved(toolName) {
		return executeToolRecovered(ctx, tool, args)
	}

	if !tool.RequiresApproval() {
		return executeToolRecovered(ctx, tool, args)
	}

	var previewStr string
	if previewer, ok := tool.(Previewer); ok {
		preview, err := previewToolRecovered(ctx, previewer, args)
		if err != nil {
			// Preview errors are validation errors — return them without executing.
			return Result{Error: fmt.Sprintf("preview error: %v", err)}, nil
		}
		previewStr = preview.String()
	}

	approved, err := g.RequestApproval(toolName, args, previewStr)
	if err != nil {
		return Result{Error: fmt.Sprintf("approval error: %v", err)}, nil
	}

	if !approved {
		return Result{
			Output: fmt.Sprintf("User denied execution of %s", toolName),
		}, nil
	}

	return executeToolRecovered(ctx, tool, args)
}

// executeToolRecovered converts a tool panic into an error so one tool cannot kill the process.
func executeToolRecovered(ctx context.Context, tool Tool, args json.RawMessage) (result Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = Result{Error: fmt.Sprintf("panic while executing %s: %v", tool.Spec().Name, r)}
			err = nil
		}
	}()
	return tool.Execute(ctx, args)
}

// previewToolRecovered is the same containment for the preview step.
func previewToolRecovered(ctx context.Context, previewer Previewer, args json.RawMessage) (preview Preview, err error) {
	defer func() {
		if r := recover(); r != nil {
			preview = Preview{}
			err = fmt.Errorf("panic while generating preview: %v", r)
		}
	}()
	return previewer.Preview(ctx, args)
}
