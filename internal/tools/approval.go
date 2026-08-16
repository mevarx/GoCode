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

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false, fmt.Errorf("failed to read input")
	}

	response := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return response == "y" || response == "yes", nil
}

func (g *ApprovalGate) WrapExecution(ctx context.Context, tool Tool, args json.RawMessage) (Result, error) {
	toolName := tool.Spec().Name

	if g.IsDenied(toolName) {
		return Result{Error: fmt.Sprintf("execution of tool %q is denied by permissions configuration", toolName)}, nil
	}

	if g.IsAutoApproved(toolName) {
		return tool.Execute(ctx, args)
	}

	if !tool.RequiresApproval() {
		return tool.Execute(ctx, args)
	}

	approved, err := g.RequestApproval(toolName, args, "")
	if err != nil {
		return Result{Error: fmt.Sprintf("approval error: %v", err)}, nil
	}

	if !approved {
		return Result{
			Output: fmt.Sprintf("User denied execution of %s", toolName),
		}, nil
	}

	return tool.Execute(ctx, args)
}

