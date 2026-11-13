// Package eval is a deterministic, network-free evaluation harness for the
// agent loop. Tests script the provider, so tool dispatch, truncation,
// approval gating, compaction, and workspace-scoped resume can be checked with
// `go test ./internal/eval` (or `make eval`) without credentials or a network.
package eval
