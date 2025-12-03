// Package main is the entry point for the qwash application.
// qwash is a PostgreSQL bloat analysis and reduction tool that provides
// non-blocking table compaction without requiring extensions.
package main

import (
	"qwash/cmd"
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
