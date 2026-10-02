package main

import "testing"

// Server commands routinely take their own flags (`npx -y ...`), so cobra must
// not parse them. GoCode's flags go before the server name; everything after
// the first positional belongs to the server.
func TestSplitMCPAddArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantServer []string
		wantConfig string
	}{
		{
			name:       "server flag -y is not ours",
			args:       []string{"demo", "npx", "-y", "@scope/server", "."},
			wantServer: []string{"demo", "npx", "-y", "@scope/server", "."},
		},
		{
			name:       "config before the server name",
			args:       []string{"--config", "/tmp/c.toml", "demo", "npx", "-y", "pkg"},
			wantServer: []string{"demo", "npx", "-y", "pkg"},
			wantConfig: "/tmp/c.toml",
		},
		{
			name:       "config=value form",
			args:       []string{"--config=/tmp/c.toml", "demo", "node", "s.js"},
			wantServer: []string{"demo", "node", "s.js"},
			wantConfig: "/tmp/c.toml",
		},
		{
			name:       "separator then server args",
			args:       []string{"sep", "docker", "--", "run", "-i", "--rm", "img"},
			wantServer: []string{"sep", "docker", "run", "-i", "--rm", "img"},
		},
		{
			name:       "config after separator, space form",
			args:       []string{"sep", "docker", "--", "run", "--config", "/tmp/c.toml"},
			wantServer: []string{"sep", "docker", "run"},
			wantConfig: "/tmp/c.toml",
		},
		{
			name:       "config after separator, equals form",
			args:       []string{"sep", "docker", "--", "run", "--config=/tmp/c.toml"},
			wantServer: []string{"sep", "docker", "run"},
			wantConfig: "/tmp/c.toml",
		},
		{
			name:       "a config flag after the server name is the server's",
			args:       []string{"demo", "npx", "--config", "/srv/conf.json"},
			wantServer: []string{"demo", "npx", "--config", "/srv/conf.json"},
		},
		{
			name:       "server with no arguments",
			args:       []string{"bare", "myserver"},
			wantServer: []string{"bare", "myserver"},
		},
		{
			name:       "verbose is ours",
			args:       []string{"--verbose", "demo", "npx", "-y", "pkg"},
			wantServer: []string{"demo", "npx", "-y", "pkg"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, configPath := splitMCPAddArgs(tc.args)

			if len(server) != len(tc.wantServer) {
				t.Fatalf("server args = %#v, want %#v", server, tc.wantServer)
			}
			for i := range server {
				if server[i] != tc.wantServer[i] {
					t.Fatalf("server args = %#v, want %#v", server, tc.wantServer)
				}
			}
			if configPath != tc.wantConfig {
				t.Errorf("configPath = %q, want %q", configPath, tc.wantConfig)
			}
		})
	}
}

// The separator must never leak into the server's argument list.
func TestSplitMCPAddArgsDropsSeparator(t *testing.T) {
	server, _ := splitMCPAddArgs([]string{"sep", "docker", "--", "run", "--rm"})
	for _, a := range server {
		if a == "--" {
			t.Fatalf("the -- separator leaked into the server args: %#v", server)
		}
	}
}
