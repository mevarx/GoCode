package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// secretSinkTool returns credential-shaped text in every Result field so the
// transcript boundary can be observed directly.
type secretSinkTool struct {
	name             string
	requiresApproval bool
}

func (s *secretSinkTool) Spec() ToolSpec {
	return ToolSpec{Name: s.name, Description: "emits secrets", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (s *secretSinkTool) Execute(context.Context, json.RawMessage) (Result, error) {
	return Result{
		Output: "stdout: " + fragOpenAI,
		Error:  "stderr: " + fragGitHub,
		Diff:   "+token = " + fragAnthropic,
	}, nil
}

func (s *secretSinkTool) RequiresApproval() bool { return s.requiresApproval }

// TestWrapExecutionAlwaysRedacts pins that the transcript boundary ignores any
// shell redaction configuration: the gate has no config flag and every result
// field is masked on both the auto-approved and the explicitly approved paths.
func TestWrapExecutionAlwaysRedacts(t *testing.T) {
	secrets := []string{fragOpenAI, fragGitHub, fragAnthropic}

	assertRedacted := func(t *testing.T, res Result) {
		t.Helper()
		for field, value := range map[string]string{
			"output": res.Output,
			"error":  res.Error,
			"diff":   res.Diff,
		} {
			if !strings.Contains(value, redactedMarker) {
				t.Errorf("%s missing %q: %q", field, redactedMarker, value)
			}
			for _, secret := range secrets {
				if strings.Contains(value, secret) {
					t.Errorf("%s leaked secret %q: %q", field, secret, value)
				}
			}
		}
	}

	t.Run("auto-approved", func(t *testing.T) {
		gate := NewApprovalGate()
		gate.SetPermissions([]string{"secret_sink"}, nil)
		tool := &secretSinkTool{name: "secret_sink"}
		res, err := gate.WrapExecution(context.Background(), tool, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertRedacted(t, res)
	})

	t.Run("approval-required", func(t *testing.T) {
		gate := NewApprovalGate()
		gate.OnPresent = func(string, json.RawMessage, string) (bool, error) { return true, nil }
		tool := &secretSinkTool{name: "secret_sink_review", requiresApproval: true}
		res, err := gate.WrapExecution(context.Background(), tool, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertRedacted(t, res)
	})
}

// TestRedactionPolicySplit documents the two layers: redact_secrets only
// governs shell output masking, while RedactToolOutput is unconditional.
func TestRedactionPolicySplit(t *testing.T) {
	input := "ANTHROPIC_API_KEY=" + fragAnthropic

	guard := &ShellGuard{RedactSecretOutput: false}
	shellOut, shellHits := guard.RedactSecrets(input)
	if shellOut != input {
		t.Errorf("disabled shell redaction changed output: %q", shellOut)
	}
	if len(shellHits) != 0 {
		t.Errorf("disabled shell redaction reported hits: %v", shellHits)
	}

	boundaryOut, boundaryHits := RedactToolOutput(input)
	if strings.Contains(boundaryOut, fragAnthropic) {
		t.Errorf("transcript redaction leaked secret: %q", boundaryOut)
	}
	if !strings.Contains(boundaryOut, redactedMarker) {
		t.Errorf("transcript redaction missing %q: %q", redactedMarker, boundaryOut)
	}
	if len(boundaryHits) == 0 {
		t.Error("transcript redaction reported no hits")
	}
}
