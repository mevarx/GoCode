package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Run drives the plain terminal loop, rendering engine events.
// Turn logic lives in RunTurn shared with the TUI: one agent implementation.
func (a *AgentLoop) Run(ctx context.Context) error {
	// One reader for prompt loop and approval gate; two scanners over stdin
	// raced for buffered bytes and broke approvals on piped input.
	stdin := bufio.NewReader(os.Stdin)
	if a.Approval != nil {
		a.Approval.SetInputReader(stdin)
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
			fmt.Printf("%s [y/N]: ", prompt)
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
			fmt.Printf("\nError: %v\n", err)
			continue
		}
	}

	if err := stdinErr(stdin); err != nil {
		return fmt.Errorf("error reading stdin: %w", err)
	}

	return nil
}

func (a *AgentLoop) renderPlain(ev LoopEvent) {
	switch ev.Kind {
	case EventDelta:
		fmt.Print(ev.Text)
	case EventNotice:
		fmt.Println(ev.Text)
	case EventExit:
		if ev.Text != "" {
			fmt.Println(ev.Text)
		}
	case EventError:
		fmt.Printf("\n[GoCode] %s\n", ev.Text)
	case EventToolResult:
		fmt.Printf("\n[%s result]\n", ev.Tool)
		if ev.IsError {
			fmt.Printf("Error: %s\n", ev.Text)
		} else {
			fmt.Println(ev.Text)
		}
	}
}

func stdinErr(r *bufio.Reader) error {
	if _, err := r.Peek(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
