package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ApprovalGate intercepts tool executions requiring user confirmation.
type ApprovalGate struct {
	OnPresent func(toolName string, args json.RawMessage, preview string) string
}

// NewApprovalGate creates a new ApprovalGate.
func NewApprovalGate() *ApprovalGate {
	return &ApprovalGate{}
}

// RequestApproval prompts the user via stdin for tool execution approval.
func (g *ApprovalGate) RequestApproval(toolName string, args json.RawMessage, preview string) (bool, error) {
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

// WrapExecution handles approval checking and execution for a tool.
func (g *ApprovalGate) WrapExecution(ctx context.Context, tool Tool, args json.RawMessage) (Result, error) {
	if !tool.RequiresApproval() {
		return tool.Execute(ctx, args)
	}

	approved, err := g.RequestApproval(tool.Spec().Name, args, "")
	if err != nil {
		return Result{Error: fmt.Sprintf("approval error: %v", err)}, nil
	}

	if !approved {
		return Result{
			Output: fmt.Sprintf("User denied execution of %s", tool.Spec().Name),
		}, nil
	}

	return tool.Execute(ctx, args)
}
