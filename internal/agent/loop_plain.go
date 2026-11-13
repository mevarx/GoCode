package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mevarx/GoCode/internal/safetext"
)

// Run drives the plain terminal loop, rendering engine events.
// Turn logic lives in RunTurn shared with the TUI: one agent implementation.
func (a *AgentLoop) Run(ctx context.Context) error {
	// One reader for prompt loop and approval gate; two scanners over stdin
	// raced for buffered bytes and broke approvals on piped input.
	stdin := bufio.NewReader(os.Stdin)
	if a.Approval != nil {
		a.Approval.SetInputReader(stdin)
		// The "e" choice reads one replacement JSON object from the same
		// reader the approval prompt used, so piped answers stay in order.
		a.Approval.EditArgs = stdinArgEditor(stdin)
	}

	a.EnsureSystemPrompt()

	fmt.Println("GoCode — Terminal Coding Agent")
	fmt.Printf("Session: %s | Provider: %s | Model: %s\n", a.Session.ID(), a.Registry.ActiveName(), a.Session.Model())
	if toolNames := a.ToolRegistry.List(); len(toolNames) > 0 {
		fmt.Printf("Tools: %s\n", strings.Join(toolNames, ", "))
	}
	fmt.Println("Type your message (or 'exit' to quit, '/help' for commands)")
	fmt.Println(strings.Repeat("─", 50))

	// Keep the previous observer so an embedding caller still sees events.
	prev := a.Observe
	a.Observe = func(ev LoopEvent) {
		a.renderPlain(ev)
		if prev != nil {
			prev(ev)
		}
	}

	for {
		fmt.Print("\n> ")

		line, readErr := stdin.ReadString('\n')
		// A final line without a trailing newline is still valid input.
		if readErr != nil && line == "" {
			break
		}
		input := strings.TrimSpace(line)

		if input == "" {
			continue
		}

		// Slash commands needing confirmation (e.g. /commit) prompt via this callback
		// on the same stdin reader.
		a.AskApproval = func(prompt string) bool {
			fmt.Printf("%s [y/N]: ", safetext.Sanitize(prompt))
			ans, err := stdin.ReadString('\n')
			if err != nil && ans == "" {
				return false
			}
			ans = strings.ToLower(strings.TrimSpace(ans))
			return ans == "y" || ans == "yes"
		}

		if err := a.RunTurn(ctx, input); err != nil {
			if IsExitRequest(err) {
				return nil
			}
			fmt.Printf("\nError: %s\n", safetext.Sanitize(err.Error()))
			continue
		}
	}

	if err := stdinErr(stdin); err != nil {
		return fmt.Errorf("error reading stdin: %w", err)
	}

	return nil
}

// stdinArgEditor builds the "e" editor for the plain loop: one JSON object
// per line from the loop's shared reader, so a piped answer sequence stays in
// order. Invalid JSON is returned as an error and the gate denies the call.
func stdinArgEditor(stdin *bufio.Reader) func(toolName string, args json.RawMessage) (json.RawMessage, error) {
	return func(toolName string, args json.RawMessage) (json.RawMessage, error) {
		fmt.Printf("Edit %s args (JSON): ", safetext.Sanitize(toolName))
		line, err := stdin.ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("failed to read edited args: %w", err)
		}
		trimmed := strings.TrimSpace(line)
		if !json.Valid([]byte(trimmed)) {
			return nil, fmt.Errorf("edited args are not valid JSON")
		}
		return json.RawMessage(trimmed), nil
	}
}

func (a *AgentLoop) renderPlain(ev LoopEvent) {
	// Model/tool text is untrusted; strip escapes so it cannot rewrite the terminal.
	text := safetext.Sanitize(ev.Text)
	tool := safetext.Sanitize(ev.Tool)
	switch ev.Kind {
	case EventDelta:
		fmt.Print(text)
	case EventNotice:
		fmt.Println(text)
	case EventExit:
		if text != "" {
			fmt.Println(text)
		}
	case EventError:
		fmt.Printf("\n[GoCode] %s\n", text)
	case EventToolResult:
		fmt.Printf("\n[%s result]\n", tool)
		if ev.IsError {
			fmt.Printf("Error: %s\n", text)
		} else {
			fmt.Println(text)
		}
	}
}

func stdinErr(r *bufio.Reader) error {
	if _, err := r.Peek(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
