package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/tools"
)

// maxHeadlessLine bounds one protocol record. A prompt with a pasted file can
// far exceed bufio.Scanner's default 64 KiB token limit.
const maxHeadlessLine = 1 << 20

// headlessPolicy is the approval allowlist used in headless mode. There is no
// human to prompt, so a tool that requires approval runs only when named in
// AutoApprove, Deny always wins, and everything else is denied.
type headlessPolicy struct {
	AutoApprove []string `json:"auto_approve"`
	Deny        []string `json:"deny"`
}

// loadHeadlessPolicy reads a policy file. An empty path or an empty file means
// "deny everything that requires approval"; an unreadable file or malformed
// JSON is a startup error.
func loadHeadlessPolicy(path string) (headlessPolicy, error) {
	if path == "" {
		return headlessPolicy{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return headlessPolicy{}, fmt.Errorf("failed to read headless policy: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return headlessPolicy{}, nil
	}
	var policy headlessPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return headlessPolicy{}, fmt.Errorf("invalid headless policy %s: %w", path, err)
	}
	return policy, nil
}

// newHeadlessApprovalGate builds the gate for a policy. Every prompt is denied
// here as well as in runHeadless, so a headless process can never block on a
// human that does not exist.
func newHeadlessApprovalGate(policy headlessPolicy) *tools.ApprovalGate {
	gate := tools.NewApprovalGateWithPermissions(policy.AutoApprove, policy.Deny)
	gate.OnPresentChoice = func(string, json.RawMessage, string) (tools.ApprovalChoice, error) {
		return tools.ApprovalDeny, nil
	}
	return gate
}

// headlessRecord is one JSON object of the stdout protocol. Text is free-form
// (already produced by the agent loop); Args is the raw tool-call JSON so a
// driver can inspect exactly what the model asked for.
type headlessRecord struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	Tool string          `json:"tool,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

// headlessToolResult keeps text and is_error unconditional, so a driver never
// has to distinguish "empty output" from "field missing".
type headlessToolResult struct {
	Type    string `json:"type"`
	Tool    string `json:"tool"`
	IsError bool   `json:"is_error"`
	Text    string `json:"text"`
}

type headlessWriter struct {
	enc *json.Encoder
}

func newHeadlessWriter(out io.Writer) *headlessWriter {
	enc := json.NewEncoder(out)
	// Tool output is arbitrary text; escaping '<' and '&' only makes it harder
	// for a driver to display.
	enc.SetEscapeHTML(false)
	return &headlessWriter{enc: enc}
}

func (w *headlessWriter) write(rec headlessRecord) error {
	return w.enc.Encode(rec)
}

// protocolToolResult records whether the tool failed, so a driver never has to
// parse the result text.
func (w *headlessWriter) toolResult(tool, text string, isError bool) error {
	return w.enc.Encode(headlessToolResult{Type: "tool_result", Tool: tool, Text: text, IsError: isError})
}

// runHeadless drives one AgentLoop over a JSONL protocol: requests on in,
// records on out. It never writes human text to out, so stdout carries only
// protocol. Returning an error makes the process exit non-zero.
func runHeadless(ctx context.Context, in io.Reader, out io.Writer, loop *agent.AgentLoop) error {
	if loop == nil {
		return errors.New("headless mode requires an agent loop")
	}

	w := newHeadlessWriter(out)

	// Defense in depth: even a loop built with a permissive gate cannot prompt
	// in headless mode. The policy gate decides what runs before this hook.
	if loop.Approval == nil {
		loop.Approval = tools.NewApprovalGate()
	}
	loop.Approval.OnPresentChoice = func(string, json.RawMessage, string) (tools.ApprovalChoice, error) {
		return tools.ApprovalDeny, nil
	}
	loop.AskApproval = func(string) bool { return false }

	// Observe cannot report errors, so the first write failure is stashed and
	// surfaced after the turn; events arrive on one goroutine, so no lock.
	var writeErr error
	loop.Observe = func(ev agent.LoopEvent) {
		if writeErr != nil {
			return
		}
		switch ev.Kind {
		case agent.EventDelta:
			writeErr = w.write(headlessRecord{Type: "delta", Text: ev.Text})
		case agent.EventNotice:
			writeErr = w.write(headlessRecord{Type: "notice", Text: ev.Text})
		case agent.EventToolStart:
			var args json.RawMessage
			if len(ev.Args) > 0 && json.Valid([]byte(ev.Args)) {
				args = json.RawMessage(ev.Args)
			}
			writeErr = w.write(headlessRecord{Type: "tool_start", Tool: ev.Tool, Args: args})
		case agent.EventToolResult:
			writeErr = w.toolResult(ev.Tool, ev.Text, ev.IsError)
		case agent.EventError:
			writeErr = w.write(headlessRecord{Type: "error", Text: ev.Text})
		}
		// EventExit and EventTurnEnd have no protocol record: exit is answered
		// by the request stream, and each turn is closed by turn_done.
	}

	// Rebuild the system prompt for this workspace, exactly as the plain loop does.
	loop.EnsureSystemPrompt()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxHeadlessLine)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var msg struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			text := fmt.Sprintf("invalid input: %v", err)
			_ = w.write(headlessRecord{Type: "error", Text: text})
			return errors.New(text)
		}

		switch msg.Type {
		case "user":
			if err := loop.RunTurn(ctx, msg.Text); err != nil {
				if agent.IsExitRequest(err) {
					_ = w.write(headlessRecord{Type: "turn_done"})
					return nil
				}
				_ = w.write(headlessRecord{Type: "error", Text: err.Error()})
				return err
			}
			if writeErr != nil {
				return writeErr
			}
			if err := w.write(headlessRecord{Type: "turn_done"}); err != nil {
				return err
			}
		case "exit":
			return nil
		default:
			text := fmt.Sprintf("unknown input type %q", msg.Type)
			_ = w.write(headlessRecord{Type: "error", Text: text})
			return errors.New(text)
		}
	}
	if err := scanner.Err(); err != nil {
		text := fmt.Sprintf("failed to read input: %v", err)
		_ = w.write(headlessRecord{Type: "error", Text: text})
		return errors.New(text)
	}

	// EOF is a clean shutdown, like exit.
	return nil
}
