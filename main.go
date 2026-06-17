// Package main is the entry point for the qwash application.
// qwash is a single-binary PostgreSQL introspection & maintenance CLI that
// detects and safely reduces table, index, and TOAST bloat without requiring
// extensions.
package main

import (
	"github.com/Alain-L/qwash/cmd"
)

// Version information (set by goreleaser at build time)
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd.Execute(version, commit, date)
}
