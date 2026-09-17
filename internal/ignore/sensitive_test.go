package ignore

import (
	"testing"
)

func TestSensitiveMatcher_DefaultPatterns(t *testing.T) {
	m := NewSensitiveMatcher(nil, nil)

	sensitiveFiles := []struct {
		path    string
		blocked bool
	}{
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		{".env.staging", true},
		{"id_rsa", true},
		{"id_ed25519", true},
		{"server.pem", true},
		{"private.key", true},
		{"credentials.json", true},
		{"token.json", true},
		{".npmrc", true},
		{".netrc", true},
		{"secret.secret", true},

		// Safe files should not be blocked.
		{"main.go", false},
		{"README.md", false},
		{"config.toml", false},
		{"package.json", false},
		{".gitignore", false},
	}

	for _, tt := range sensitiveFiles {
		t.Run(tt.path, func(t *testing.T) {
			pattern := m.IsSensitive(tt.path)
			blocked := pattern != ""
			if blocked != tt.blocked {
				if tt.blocked {
					t.Errorf("expected %q to be blocked as sensitive, but it was not", tt.path)
				} else {
					t.Errorf("expected %q to NOT be blocked, but matched pattern %q", tt.path, pattern)
				}
			}
		})
	}
}

func TestSensitiveMatcher_PathPatterns(t *testing.T) {
	m := NewSensitiveMatcher(nil, nil)

	pathTests := []struct {
		path    string
		blocked bool
	}{
		{"/home/user/.aws/credentials", true},
		{"/home/user/.docker/config.json", true},
		{"/home/user/.kube/config", true}, // matches ".kube/config" sensitive pattern
		{"src/.env", true},
		{"deploy/.env.production", true},
	}

	for _, tt := range pathTests {
		t.Run(tt.path, func(t *testing.T) {
			pattern := m.IsSensitive(tt.path)
			blocked := pattern != ""
			if blocked != tt.blocked {
				if tt.blocked {
					t.Errorf("expected %q to be blocked, but it was not", tt.path)
				} else {
					t.Errorf("expected %q to NOT be blocked, but matched pattern %q", tt.path, pattern)
				}
			}
		})
	}
}

func TestSensitiveMatcher_ExtraPatterns(t *testing.T) {
	m := NewSensitiveMatcher(nil, []string{"custom-secret.yaml", "*.vault"})

	if p := m.IsSensitive("custom-secret.yaml"); p == "" {
		t.Error("expected custom-secret.yaml to be blocked by extra pattern")
	}
	if p := m.IsSensitive("data.vault"); p == "" {
		t.Error("expected data.vault to be blocked by extra pattern")
	}
}

func TestSensitiveMatcher_ShouldBlock(t *testing.T) {
	inner := &dummyMatcher{ignoredPaths: map[string]bool{"ignored.txt": true}}
	m := NewSensitiveMatcher(inner, nil)

	// Ignored by inner matcher.
	blocked, reason := m.ShouldBlock("ignored.txt", false)
	if !blocked {
		t.Error("expected ignored.txt to be blocked")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}

	// Sensitive file.
	blocked, reason = m.ShouldBlock(".env", false)
	if !blocked {
		t.Error("expected .env to be blocked")
	}
	if reason == "" {
		t.Error("expected non-empty reason for sensitive file")
	}

	// Normal file.
	blocked, _ = m.ShouldBlock("main.go", false)
	if blocked {
		t.Error("expected main.go to NOT be blocked")
	}
}

type dummyMatcher struct {
	ignoredPaths map[string]bool
}

func (d *dummyMatcher) IsIgnored(path string, isDir bool) bool {
	return d.ignoredPaths[path]
}
