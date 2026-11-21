package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Decision is a hook's opinion about the call or lifecycle step.
type Decision int

const (
	// DecisionNone means the hook expressed no opinion (success without a
	// decision, timeout, cancellation, or a non-blocking error exit).
	DecisionNone Decision = iota
	// DecisionAllow approves the call. On PreToolUse it short-circuits the
	// permission prompt for THAT call only; it never bypasses the deny-list.
	DecisionAllow
	// DecisionDeny blocks the call (or compaction, on PreCompact).
	DecisionDeny
)

// String returns the wire name of a decision.
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	}
	return "none"
}

// HookResult is one hook's classified outcome, in configuration order.
type HookResult struct {
	Name     string
	Event    Event
	Matcher  string
	Command  string
	Decision Decision
	Halt     bool
	Reason   string
	// StopReason carries the folded continue:false stopReason, if any.
	StopReason string
	Context    string
	// UpdatedInput is the raw patch object; aggregation shallow-merges it.
	UpdatedInput json.RawMessage
	// UpdatedOutput is a whole replacement of the tool output; nil = unset.
	UpdatedOutput *string
	// Diagnostic is non-empty when the hook produced no opinion for an
	// abnormal reason (timeout, abandon, cancel, start failure, error exit).
	Diagnostic string
}

// Aggregated is the combined outcome of one event firing.
type Aggregated struct {
	// Decision follows deny > allow > none.
	Decision Decision
	// Halt is the sticky OR of every hook's halt.
	Halt bool
	// Reason joins deny reasons (and halting non-deny reasons) with newlines,
	// in config order.
	Reason string
	// StopReason is the last non-empty continue:false stopReason.
	StopReason string
	// Context joins every hook's context with newlines; the caller appends it
	// to the tool result the model sees.
	Context string
	// UpdatedInput is the original input shallow-merged with every valid
	// patch in config order; nil when no patch applied.
	UpdatedInput json.RawMessage
	// UpdatedOutput is the last hook's whole replacement; nil when unset.
	UpdatedOutput *string
	// Results carries every hook outcome in config order.
	Results []HookResult
	// Diagnostics collects abnormal outcomes; the caller decides how to log
	// them (the engine has no logger dependency).
	Diagnostics []string
}

// Aggregate combines hook results in config order:
// - decision: deny > allow > none (strongest wins)
// - halt: sticky OR
// - reason: newline-joined deny reasons; a halting non-deny hook contributes
// - context: newline-joined, appended to the tool result by the caller
// - updated_input: shallow merge of top-level keys onto the original input,
// applied in order; a patch that is not a JSON object is rejected with a
// diagnostic
// - updated_output: whole replacement, last hook to set it wins
func Aggregate(results []HookResult, originalInput json.RawMessage) Aggregated {
	agg := Aggregated{Results: results}
	var reasons, contexts []string
	var patches []json.RawMessage
	for _, r := range results {
		if r.Diagnostic != "" {
			agg.Diagnostics = append(agg.Diagnostics, r.Diagnostic)
		}
		switch {
		case r.Decision == DecisionDeny:
			agg.Decision = DecisionDeny
		case r.Decision == DecisionAllow && agg.Decision != DecisionDeny:
			agg.Decision = DecisionAllow
		}
		agg.Halt = agg.Halt || r.Halt
		if r.Reason != "" && (r.Decision == DecisionDeny || r.Halt) {
			reasons = append(reasons, r.Reason)
		}
		if r.Context != "" {
			contexts = append(contexts, r.Context)
		}
		if len(r.UpdatedInput) > 0 {
			patches = append(patches, r.UpdatedInput)
		}
		if r.UpdatedOutput != nil {
			agg.UpdatedOutput = r.UpdatedOutput
		}
		if r.StopReason != "" {
			agg.StopReason = r.StopReason
		}
	}
	agg.Reason = strings.Join(reasons, "\n")
	agg.Context = strings.Join(contexts, "\n")

	if len(patches) > 0 {
		base := parseObject(originalInput)
		if base == nil {
			base = map[string]json.RawMessage{}
		}
		merged := false
		for _, patch := range patches {
			obj := parseObject(patch)
			if obj == nil {
				agg.Diagnostics = append(agg.Diagnostics,
					"updated_input is not a JSON object; patch rejected")
				continue
			}
			for k, v := range obj {
				base[k] = v
			}
			merged = true
		}
		if merged {
			if out, err := json.Marshal(base); err == nil {
				agg.UpdatedInput = out
			}
		}
	}
	return agg
}

// parseObject decodes a strict JSON object. Non-objects (including null,
// arrays and scalars) and invalid JSON return nil. RawMessage values keep
// numeric literals exact through the merge.
func parseObject(raw []byte) map[string]json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil
	}
	return obj
}
