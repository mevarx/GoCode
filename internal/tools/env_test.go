package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func envKey(entry string) string {
	if i := strings.IndexByte(entry, '='); i >= 0 {
		return entry[:i]
	}
	return entry
}

func envContainsKeyFold(env []string, name string) bool {
	for _, entry := range env {
		if strings.EqualFold(envKey(entry), name) {
			return true
		}
	}
	return false
}

func envContainsValue(env []string, value string) bool {
	for _, entry := range env {
		if strings.Contains(entry, value) {
			return true
		}
	}
	return false
}

func TestScrubbedEnvDropsCredentialPatterns(t *testing.T) {
	t.Setenv("MY_GATEWAY_KEY", "gateway-secret-value")
	t.Setenv("OPENAI_API_KEY", "sk-test-openai-value")
	t.Setenv("CUSTOM_SECRET", "custom-secret-value")
	t.Setenv("KEEP_ME", "keep-me-value")

	env := ScrubbedEnv(nil)

	for _, name := range []string{"MY_GATEWAY_KEY", "OPENAI_API_KEY", "CUSTOM_SECRET"} {
		if envContainsKeyFold(env, name) {
			t.Errorf("expected %s to be scrubbed, env=%v", name, env)
		}
	}
	for _, value := range []string{"gateway-secret-value", "sk-test-openai-value", "custom-secret-value"} {
		if envContainsValue(env, value) {
			t.Errorf("expected secret value %q to be absent, env=%v", value, env)
		}
	}
	if !envContainsKeyFold(env, "KEEP_ME") {
		t.Errorf("expected KEEP_ME to survive scrubbing")
	}
	if !envContainsKeyFold(env, "PATH") {
		t.Errorf("expected allowlisted PATH to survive scrubbing")
	}
}

func TestScrubbedEnvExtraNamesAndEssentialWins(t *testing.T) {
	t.Setenv("KEEP_ME", "keep-me-value")

	env := ScrubbedEnv([]string{"KEEP_ME"})
	if envContainsKeyFold(env, "KEEP_ME") {
		t.Errorf("expected extra name KEEP_ME to be scrubbed")
	}

	// Essential variables win even when listed in extra.
	env = ScrubbedEnv([]string{"PATH"})
	if !envContainsKeyFold(env, "PATH") {
		t.Errorf("expected allowlisted PATH to survive an extra scrub entry")
	}
}

func TestScrubbedEnvExtraPatterns(t *testing.T) {
	t.Setenv("MY_GATEWAY_KEY", "gateway-secret-value")

	env := ScrubbedEnv([]string{"MY_*_KEY"})
	if envContainsKeyFold(env, "MY_GATEWAY_KEY") {
		t.Errorf("expected extra pattern MY_*_KEY to scrub MY_GATEWAY_KEY")
	}
}

func TestScrubEnvPreservesMalformedEntriesAndOrder(t *testing.T) {
	environ := []string{"FIRST=1", "MALFORMED", "SECOND=2"}
	got := scrubEnv(environ, nil)
	want := []string{"FIRST=1", "MALFORMED", "SECOND=2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestShellExecDoesNotLeakSecretsToChild(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-shell-leak-openai")
	t.Setenv("MY_GATEWAY_KEY", "shell-leak-gateway")
	t.Setenv("GOCODE_ENV_KEEP", "gocode-env-keep-value")

	tool := &ShellExecTool{Timeout: 10 * time.Second}
	command := "env"
	if runtime.GOOS == "windows" {
		command = "set"
	}
	args, err := json.Marshal(shellExecArgs{Command: command})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s\noutput: %s", res.Error, res.Output)
	}

	for _, leaked := range []string{"sk-shell-leak-openai", "shell-leak-gateway", "OPENAI_API_KEY", "MY_GATEWAY_KEY"} {
		if strings.Contains(res.Output, leaked) {
			t.Errorf("shell child leaked %q; output:\n%s", leaked, res.Output)
		}
	}
	if !strings.Contains(res.Output, "GOCODE_ENV_KEEP") {
		t.Errorf("expected non-credential variable to reach child; output:\n%s", res.Output)
	}
}
