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
	"time"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/pubsub"
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
	// AutoApproveSession opts the session into never-prompting (additive):
	// every non-denied approval-gated call is granted with a Granted
	// notification. Default false keeps the deny-by-default contract.
	AutoApproveSession bool `json:"auto_approve_session"`
	// DangerousTools extends the danger classifier's per-tool list (additive).
	DangerousTools []string `json:"dangerous_tools"`
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
	gate.SetDangerTools(policy.DangerousTools)
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

// headlessTurnEndWait bounds the turn goroutine's wait for the bus pump to
// consume EventTurnEnd. The normal handoff takes microseconds; the fallback
// only fires if the must-deliver lane dropped the terminal event to a stalled
// writer (counted in MustDeliverDropCount) — 20× the 50ms bound, generous for
// slow hosts, and it keeps turn_done from hanging on a lost event.
const headlessTurnEndWait = time.Second

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

	// headless consumes the SAME bus as the TUI instead of a private
	// Observe closure. One pump goroutine maps events to protocol records in
	// bus FIFO order; the JSONL schema is unchanged (v0.7.5 contract). The
	// pump owns lastTurn and the endSeen handshake, shared with the turn
	// goroutine under stateMu.
	bus := pubsub.NewBroker[agent.LoopEvent]()
	loop.Bus = bus
	busEvents, busUnsub := bus.Subscribe()

	var stateMu sync.Mutex
	var lastTurn *headlessTurnMetrics
	var endSeen chan struct{}
	// settleEnds counts consumed settlement events (Cancelled turn ends).
	// A settlement belongs to a dropped prompt, never to the running turn:
	// it must not satisfy the running turn's endSeen handshake or overwrite
	// its metrics. A turn that settles at entry waits on this counter instead.
	var settleEnds int

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for ev := range busEvents {
			switch ev.Payload.Kind {
			case agent.EventDelta:
				_ = w.write(headlessRecord{Type: "delta", Text: ev.Payload.Text})
			case agent.EventNotice:
				_ = w.write(headlessRecord{Type: "notice", Text: ev.Payload.Text})
			case agent.EventToolStart:
				var args json.RawMessage
				if len(ev.Payload.Args) > 0 && json.Valid([]byte(ev.Payload.Args)) {
					args = json.RawMessage(ev.Payload.Args)
				}
				_ = w.write(headlessRecord{Type: "tool_start", Tool: ev.Payload.Tool, Args: args})
			case agent.EventToolDelta:
				_ = w.encode(headlessToolDelta{
					Type:       "tool_delta",
					ToolCallID: ev.Payload.ToolCallID,
					Name:       ev.Payload.Tool,
					ArgsSoFar:  ev.Payload.Args,
				})
			case agent.EventToolResult:
				_ = w.toolResult(ev.Payload.Tool, ev.Payload.Text, ev.Payload.IsError)
			case agent.EventError:
				_ = w.write(headlessRecord{Type: "error", Text: ev.Payload.Text})
			case agent.EventTurnEnd:
				stateMu.Lock()
				if ev.Payload.Cancelled {
					// settlement: terminal for a dropped prompt only.
					settleEnds++
				} else {
					lastTurn = headlessMetrics(ev.Payload.Metrics)
					if endSeen != nil {
						close(endSeen)
						endSeen = nil
					}
				}
				stateMu.Unlock()
			}
			// EventTurnStart and EventExit have no protocol record: a turn is
			// opened implicitly by its first record and closed by turn_done.
		}
	}()
	defer func() {
		bus.Close()
		busUnsub()
		<-pumpDone
	}()

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
	// queued user records live in the engine's dispatcher (FIFO, with
	// accept sequences and the cancel high-water mark), not a private slice:
	// cancel-on-entry settles dropped records and piped input never hangs.

	startTurn := func(text string, seq uint64) {
		stateMu.Lock()
		lastTurn = nil
		endSeen = make(chan struct{})
		seen := endSeen
		baseSettles := settleEnds
		stateMu.Unlock()
		resCh := make(chan headlessTurnResult, 1)
		results = resCh
		go func() {
			ran, err := loop.RunTurnAccepted(ctx, text, seq)
			// The pump owns lastTurn now: wait for it to consume the turn's
			// terminal event so turn_done follows every record of the turn
			// and carries its metrics (the v0.7.5 synchronous-Observe order).
			// Empty input never starts a turn, so there is nothing to wait
			// for. A turn that ran waits on endSeen (its primary end event);
			// a cancel-on-entry settlement waits on the settlement counter
			// instead — settlements never touch endSeen. The timeout only
			// fires on a counted must-deliver drop.
			if strings.TrimSpace(text) != "" {
				if ran {
					select {
					case <-seen:
					case <-ctx.Done():
					case <-time.After(headlessTurnEndWait):
					}
				} else {
					deadline := time.Now().Add(headlessTurnEndWait)
					for {
						stateMu.Lock()
						n := settleEnds
						stateMu.Unlock()
						if n > baseSettles || ctx.Err() != nil || time.Now().After(deadline) {
							break
						}
						time.Sleep(time.Millisecond)
					}
				}
			}
			stateMu.Lock()
			metrics := lastTurn
			stateMu.Unlock()
			switch {
			case err == nil:
				_ = w.write(headlessRecord{Type: "turn_done", Metrics: metrics})
				resCh <- headlessTurnResult{}
			case agent.IsExitRequest(err):
				_ = w.write(headlessRecord{Type: "turn_done", Metrics: metrics})
				resCh <- headlessTurnResult{exit: true}
			case errors.Is(err, context.Canceled) && ctx.Err() == nil:
				// user cancel: a clean terminal record, not a protocol
				// failure — the driver keeps its per-prompt accounting and
				// the session keeps serving. (A cancelled parent ctx is a
				// shutdown and still falls through to the error path.)
				_ = w.write(headlessRecord{Type: "turn_done", Metrics: metrics})
				resCh <- headlessTurnResult{}
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
		if text, seq, ok := loop.NextQueuedTurn(); ok {
			startTurn(text, seq)
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
		// Belt and braces: anything still queued with no turn running (a
		// cancel settled the queue, or acceptance raced the last result) is
		// drained too, so EOF never leaves an accepted prompt unanswered.
		for {
			text, seq, ok := loop.NextQueuedTurn()
			if !ok {
				return nil
			}
			startTurn(text, seq)
			exit, err := advance()
			if err != nil {
				return err
			}
			if exit {
				return nil
			}
		}
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
				// accept the prompt NOW (monotonic sequence), then either
				// start it or queue it in the dispatcher. A cancel landing
				// between this accept and the turn's entry is honored by the
				// mark test (cancel-on-entry) and still emits turn_done.
				seq := loop.BeginAccepted()
				if results != nil {
					loop.EnqueueTurn(seq, msg.Text)
					continue
				}
				startTurn(msg.Text, seq)

			case "steer":
				if strings.TrimSpace(msg.Text) == "" {
					continue
				}
				if !loop.Steer(msg.Text) {
					_ = w.write(headlessRecord{Type: "notice", Text: headlessSteerIdleNotice})
				}

			case "cancel":
				// (additive input type): raise the cancel mark, cancel the
				// running turn cooperatively and settle every queued prompt.
				// Each dropped record gets a terminal turn_done so a piped
				// driver's per-prompt accounting never hangs; the running turn
				// writes its own turn_done when it winds down.
				for range loop.CancelTurns() {
					_ = w.write(headlessRecord{Type: "turn_done"})
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
