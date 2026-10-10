// file: main.go
// version: 1.8.0
// guid: 5f6a7b8c-9d0e-1f2a-3b4c-5d6e7f8a9b0c
// last-edited: 2026-10-09

package main

import (
	"fmt"
	"os"

	"github.com/falkcorp/audiobook-organizer/cmd"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

var executeCmd = cmd.Execute

func main() {
	os.Exit(run())
}

func run() int {
	// Set version everywhere
	cmd.SetVersion(version)
	server.SetVersion(version)
	telemetry.SetVersion(version)
	server.SetEmbeddedFS(WebFS)

	// `make rollback` asks the previous binary which storage format it
	// supports. Handled before cobra parses (like the child-mode sentinel
	// below) because cobra's PersistentPreRun prints "Using config file: ..."
	// to stdout, and this output must be exactly one line.
	if len(os.Args) == 2 && os.Args[1] == "--print-storage-format" {
		fmt.Println(database.SupportedStorageFormat)
		return 0
	}

	// MAYDEPLOY-A: operation-runner child mode must be detected BEFORE
	// cobra parses os.Args, because --operation-runner is a sentinel arg
	// (not a registered cobra flag) and would otherwise cause cobra to
	// exit with "unknown flag". cmd.RunOperationRunner builds a minimal
	// server (registers all OperationDefs) and then hands off to
	// registry.RunChildMode, which never returns.
	if registry.IsChildMode() {
		cmd.RunOperationRunner()
		// Unreachable — RunOperationRunner calls os.Exit.
		return 0
	}

	if err := executeCmd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
