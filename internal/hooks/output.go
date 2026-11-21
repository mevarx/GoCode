package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
)

// rawOutput mirrors the stdout JSON contract. Every field is
// optional; hookSpecificOutput carries the Claude Code PreToolUse shape.
type rawOutput struct {
	Version  int    `json:"version"`
	Decision string `json:"decision"`
	Halt     bool   `json:"halt"`
	Reason   string `json:"reason"`
	// Context is a string or an array of strings.
	Context       json.RawMessage `json:"context"`
	UpdatedInput  json.RawMessage `json:"updated_input"`
	UpdatedOutput *string         `json:"updated_output"`
	Continue      *bool           `json:"continue"`
	StopReason    string          `json:"stopReason"`
	HookSpecific  *struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		UpdatedToolOutput        *string         `json:"updatedToolOutput"`
		AdditionalContext        string          `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// parseOutput decodes stdout into the output contract. Only a valid JSON
// OBJECT counts; empty output, scalars, arrays and malformed JSON report
// false so the exit-code semantics take over.
func parseOutput(stdout []byte) (rawOutput, bool) {
	var out rawOutput
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return out, false
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return out, false
	}
	return out, true
}

// applyOutput folds a parsed stdout JSON object into a hook result:
// - decision allow/approve → allow, deny/block → deny; the top-level
// decision wins over hookSpecificOutput.permissionDecision, whose "ask"
// maps to no opinion (the normal permission prompt proceeds)
// - continue:false folds to halt:true with stopReason as the reason fallback
// - context accepts a string or a string array (newline-joined);
// hookSpecificOutput.additionalContext is appended
// - updated_input/updated_output fall back to the hookSpecificOutput shapes
func applyOutput(base HookResult, out rawOutput) HookResult {
	base.Decision = decisionFromWord(out.Decision)
	if base.Decision == DecisionNone && out.HookSpecific != nil {
		base.Decision = decisionFromWord(out.HookSpecific.PermissionDecision)
	}
	base.Halt = out.Halt
	base.Reason = out.Reason
	base.StopReason = out.StopReason

	if out.Continue != nil && !*out.Continue {
		base.Halt = true
		if base.Reason == "" {
			base.Reason = out.StopReason
		}
	}
	if base.Reason == "" && out.HookSpecific != nil && out.HookSpecific.PermissionDecisionReason != "" {
		base.Reason = out.HookSpecific.PermissionDecisionReason
	}

	base.Context = contextText(out.Context)
	if out.HookSpecific != nil && out.HookSpecific.AdditionalContext != "" {
		if base.Context == "" {
			base.Context = out.HookSpecific.AdditionalContext
		} else {
			base.Context += "\n" + out.HookSpecific.AdditionalContext
		}
	}

	base.UpdatedInput = out.UpdatedInput
	if len(base.UpdatedInput) == 0 && out.HookSpecific != nil {
		base.UpdatedInput = out.HookSpecific.UpdatedInput
	}
	base.UpdatedOutput = out.UpdatedOutput
	if base.UpdatedOutput == nil && out.HookSpecific != nil {
		base.UpdatedOutput = out.HookSpecific.UpdatedToolOutput
	}
	return base
}

// decisionFromWord maps the wire decision vocabulary. "ask" and unknown
// words are no opinion: the normal flow decides.
func decisionFromWord(word string) Decision {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "allow", "approve":
		return DecisionAllow
	case "deny", "block":
		return DecisionDeny
	}
	return DecisionNone
}

// contextText renders the string-or-array context field. An array joins its
// string elements with newlines; non-string elements are skipped. Any other
// shape contributes nothing.
func contextText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return ""
	}
	var parts []string
	for _, item := range arr {
		var elem string
		if err := json.Unmarshal(item, &elem); err == nil && elem != "" {
			parts = append(parts, elem)
		}
	}
	return strings.Join(parts, "\n")
}

// classifyExit applies the exact exit-code semantics:
// - 0: success; a valid stdout JSON object is honoured, else no opinion
// - 2: deny; stderr is the reason (fallback "blocked by hook")
// - 49: deny AND halt; stderr is the reason (fallback "turn halted by hook")
// - any other non-zero: non-blocking error (no opinion) UNLESS stdout
// carried a valid JSON object, which is then honoured and the exit code
// ignored
//
// On 2 and 49 the exit code is the decision; stdout does not override it.
func classifyExit(base HookResult, stdout, stderr []byte, exitCode int) HookResult {
	switch exitCode {
	case 0:
		if out, ok := parseOutput(stdout); ok {
			return applyOutput(base, out)
		}
		return base
	case 2:
		base.Decision = DecisionDeny
		base.Reason = stderrReason(stderr, "blocked by hook")
		return base
	case HaltExitCode:
		base.Decision = DecisionDeny
		base.Halt = true
		base.Reason = stderrReason(stderr, "turn halted by hook")
		return base
	default:
		if out, ok := parseOutput(stdout); ok {
			return applyOutput(base, out)
		}
		detail := string(bytes.TrimSpace(stderr))
		if detail == "" {
			detail = "no stderr"
		}
		base.Diagnostic = diagnosticf("hook %q exited %d (non-blocking): %s", base.label(), exitCode, detail)
		return base
	}
}

// label names the hook in diagnostics (name if set, else command).
func (r HookResult) label() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Command
}
