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
	"strings"
	"sync"

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
// driver can inspect exactly what the model asked for. Metrics is additive on
// turn_done: older drivers that ignore it keep working.
type headlessRecord struct {
	Type    string               `json:"type"`
	Text    string               `json:"text,omitempty"`
	Tool    string               `json:"tool,omitempty"`
	Args    json.RawMessage      `json:"args,omitempty"`
	Metrics *headlessTurnMetrics `json:"metrics,omitempty"`
}

// headlessTurnMetrics is the stable metrics schema on turn_done. Token counts
// come from the provider when it reported usage, from local estimates
// otherwise; provider_reported says which.
type headlessTurnMetrics struct {
	PromptTokens     int   `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	ToolCalls        int   `json:"tool_calls"`
	ElapsedMS        int64 `json:"elapsed_ms"`
	Retries          int64 `json:"retries"`
	ProviderReported bool  `json:"provider_reported"`
	ContextTokens    int   `json:"context_tokens"`
	ContextBudget    int   `json:"context_budget"`
}

// headlessMetrics converts the engine's turn summary to the protocol shape.
func headlessMetrics(m *agent.TurnMetrics) *headlessTurnMetrics {
	if m == nil {
		return nil
	}
	return &headlessTurnMetrics{
		PromptTokens:     m.PromptTokens,
		CompletionTokens: m.CompletionTokens,
		ToolCalls:        m.ToolCalls,
		ElapsedMS:        m.Elapsed.Milliseconds(),
		Retries:          m.Retries,
		ProviderReported: m.ProviderReported,
		ContextTokens:    m.ContextTokens,
		ContextBudget:    m.ContextBudget,
	}
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
	mu  sync.Mutex
	enc *json.Encoder
	err error
}

func newHeadlessWriter(out io.Writer) *headlessWriter {
	enc := json.NewEncoder(out)
	// Tool output is arbitrary text; escaping '<' and '&' only makes it harder
	// for a driver to display.
	enc.SetEscapeHTML(false)
	return &headlessWriter{enc: enc}
}

// encode serialises one record. A turn runs on its own goroutine while the
// reader keeps accepting steering, so every write is serialised; the first
// failure is sticky and surfaced once the turn ends.
func (w *headlessWriter) encode(rec any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if err := w.enc.Encode(rec); err != nil {
		w.err = err
		return err
	}
	return nil
}

func (w *headlessWriter) write(rec headlessRecord) error {
	return w.encode(rec)
}

// protocolToolResult records whether the tool failed, so a driver never has to
// parse the result text.
func (w *headlessWriter) toolResult(tool, text string, isError bool) error {
	return w.encode(headlessToolResult{Type: "tool_result", Tool: tool, Text: text, IsError: isError})
}

// failed reports the first write error, if any.
func (w *headlessWriter) failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// headlessToolDelta is the additive tool_delta frame: args_so_far is the
// accumulated, sanitized argument preview, never a partial JSON value a driver
// should execute. Drivers that ignore it keep working.
type headlessToolDelta struct {
	Type       string `json:"type"`
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	ArgsSoFar  string `json:"args_so_far"`
}

// Steering refusals are notices, not errors: an idle steer is a no-op, not a
// protocol violation.
const headlessSteerIdleNotice = "[Steering not applied: no turn is running.]"

// headlessTurnResult reports how a background turn ended.
type headlessTurnResult struct {
	exit bool
	err  error
}

// runHeadless drives one AgentLoop over a JSONL protocol: requests on in,
// records on out. It never writes human text to out, so stdout carries only
// protocol. Returning an error makes the process exit non-zero.
//
// A user record starts its turn on a goroutine so the reader stays free to
// accept steer records while the model is streaming; turn_done still closes
// every turn exactly once.
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

	// lastTurn holds the metrics from the turn that just ended, so turn_done
	// can carry them. Observe and the turn-completion write run on the turn
	// goroutine, so this needs no lock.
	var lastTurn *headlessTurnMetrics
	loop.Observe = func(ev agent.LoopEvent) {
		switch ev.Kind {
		case agent.EventDelta:
			_ = w.write(headlessRecord{Type: "delta", Text: ev.Text})
		case agent.EventNotice:
			_ = w.write(headlessRecord{Type: "notice", Text: ev.Text})
		case agent.EventToolStart:
			var args json.RawMessage
			if len(ev.Args) > 0 && json.Valid([]byte(ev.Args)) {
				args = json.RawMessage(ev.Args)
			}
			_ = w.write(headlessRecord{Type: "tool_start", Tool: ev.Tool, Args: args})
		case agent.EventToolDelta:
			_ = w.encode(headlessToolDelta{
				Type:       "tool_delta",
				ToolCallID: ev.ToolCallID,
				Name:       ev.Tool,
				ArgsSoFar:  ev.Args,
			})
		case agent.EventToolResult:
			_ = w.toolResult(ev.Tool, ev.Text, ev.IsError)
		case agent.EventError:
			_ = w.write(headlessRecord{Type: "error", Text: ev.Text})
		case agent.EventTurnEnd:
			lastTurn = headlessMetrics(ev.Metrics)
		}
		// EventTurnStart and EventExit have no protocol record: a turn is
		// opened implicitly by its first record and closed by turn_done.
	}

	// Rebuild the system prompt for this workspace, exactly as the plain loop does.
	loop.EnsureSystemPrompt()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxHeadlessLine)

	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-stop:
				return
			}
		}
		scanErr <- scanner.Err()
		close(lines)
	}()

	// results is non-nil exactly while a turn goroutine is running. A nil
	// channel disables its select case.
	var results chan headlessTurnResult
	// pending holds user records that arrived while a turn was running; they
	// run in order after it, preserving piped-input semantics.
	var pending []string

	startTurn := func(text string) {
		lastTurn = nil
		resCh := make(chan headlessTurnResult, 1)
		results = resCh
		go func() {
			err := loop.RunTurn(ctx, text)
			switch {
			case err == nil:
				_ = w.write(headlessRecord{Type: "turn_done", Metrics: lastTurn})
				resCh <- headlessTurnResult{}
			case agent.IsExitRequest(err):
				_ = w.write(headlessRecord{Type: "turn_done", Metrics: lastTurn})
				resCh <- headlessTurnResult{exit: true}
			default:
				_ = w.write(headlessRecord{Type: "error", Text: err.Error()})
				resCh <- headlessTurnResult{err: err}
			}
		}()
	}

	// handleResult consumes one finished turn and starts the next queued one.
	// It reports an exit request or the first failure.
	handleResult := func(res headlessTurnResult) (bool, error) {
		results = nil
		if res.err != nil {
			return false, res.err
		}
		if err := w.failed(); err != nil {
			return false, err
		}
		if res.exit {
			return true, nil
		}
		if len(pending) > 0 {
			text := pending[0]
			pending = pending[1:]
			startTurn(text)
		}
		return false, nil
	}

	advance := func() (bool, error) {
		return handleResult(<-results)
	}

	// finishTurns drains the running turn and everything queued behind it.
	finishTurns := func() error {
		for results != nil {
			exit, err := advance()
			if err != nil {
				return err
			}
			if exit {
				return nil
			}
		}
		return nil
	}

	for {
		select {
		case raw, ok := <-lines:
			if !ok {
				// EOF is a clean shutdown like exit, but a running turn must
				// finish first so its records are not truncated.
				if err := finishTurns(); err != nil {
					return err
				}
				if err := <-scanErr; err != nil {
					text := fmt.Sprintf("failed to read input: %v", err)
					_ = w.write(headlessRecord{Type: "error", Text: text})
					return errors.New(text)
				}
				return nil
			}

			line := bytes.TrimSpace(raw)
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
				// A turn may have completed while this line was in flight;
				// consume that result first so the next turn starts cleanly.
				select {
				case res := <-results:
					exit, err := handleResult(res)
					if err != nil {
						return err
					}
					if exit {
						return nil
					}
				default:
				}
				if results != nil {
					pending = append(pending, msg.Text)
					continue
				}
				startTurn(msg.Text)

			case "steer":
				if strings.TrimSpace(msg.Text) == "" {
					continue
				}
				if !loop.Steer(msg.Text) {
					_ = w.write(headlessRecord{Type: "notice", Text: headlessSteerIdleNotice})
				}

			case "exit":
				if err := finishTurns(); err != nil {
					return err
				}
				return nil

			default:
				text := fmt.Sprintf("unknown input type %q", msg.Type)
				_ = w.write(headlessRecord{Type: "error", Text: text})
				return errors.New(text)
			}

		case res := <-results:
			if _, err := handleResult(res); err != nil {
				return err
			}
		}
	}
}
