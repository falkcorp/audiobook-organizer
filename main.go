// file: main.go
// version: 1.11.0
// guid: 5f6a7b8c-9d0e-1f2a-3b4c-5d6e7f8a9b0c
// last-edited: 2026-11-08

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/falkcorp/audiobook-organizer/cmd"
	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/buildid"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

var executeCmd = cmd.Execute

// Fast-exit flags of the release tooling (09 §9.4 R1). Like the two above them
// they are handled before cobra parses and before any store or config opens,
// need no credential, and are matched only as the one and only argument.
const (
	versionJSONFlag       = "--version-json"
	printUpgradeFloorFlag = "--print-upgrade-floor"
)

// uiBuildDev is the UI build ID of a binary with no embedded bundle, and the
// value the server's X-App-Build carries then (05-http-api stage 5).
const uiBuildDev = "dev"

// versionInfo is the one field list of --version-json (06 §6.4.5, FE6-4):
// exactly these five fields, in this order, all always present. The updater's
// step-3 self-test (05-updater) reads the output with this list, so commit is
// "" rather than absent for a build without VCS data.
type versionInfo struct {
	App       string `json:"app"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	UIBuildID string `json:"ui_build_id"`
	BinaryID  string `json:"binary_id"`
}

// currentVersionInfo collects the five fields: the app ID, the release
// version, buildid.Commit (vcs.revision, "+dirty" when modified, "" when the
// build has none), the UI build ID of the embedded bundle (the value
// X-App-Build reports: the bundle's meta tag, or "dev" without one), and the
// binary ID (app.BinaryID: the version plus the executable's SHA-256).
func currentVersionInfo() (versionInfo, error) {
	binaryID, err := app.BinaryID(app.Options{Version: version})
	if err != nil {
		return versionInfo{}, err
	}
	ui := server.BuildIDFromFS(WebFS)
	if ui == "" {
		ui = uiBuildDev
	}
	return versionInfo{
		App:       books.AppID,
		Version:   version,
		Commit:    buildid.Commit(),
		UIBuildID: ui,
		BinaryID:  binaryID,
	}, nil
}

// fastExit answers --version-json and --print-upgrade-floor. handled is true
// when args was exactly one of them and its answer went to stdout; the caller
// then exits without starting anything, with status 1 when err is not nil.
//
// --print-upgrade-floor prints nothing: no build declares an upgrade floor
// until R3 (02 N14), and an empty answer is "no floor" (05-storage §5S.3.7).
func fastExit(args []string, stdout io.Writer) (handled bool, err error) {
	if len(args) != 2 {
		return false, nil
	}
	switch args[1] {
	case versionJSONFlag:
		info, err := currentVersionInfo()
		if err != nil {
			return true, err
		}
		b, err := json.Marshal(info)
		if err != nil {
			return true, err
		}
		_, err = fmt.Fprintln(stdout, string(b))
		return true, err
	case printUpgradeFloorFlag:
		return true, nil
	}
	return false, nil
}

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

	// The deploy and rollback recipes ask a binary what it supports before
	// restarting it (scripts/deploy-match-unit-type.sh): `--print-capabilities`
	// prints one token per line, and `sd_notify` means the binary sends
	// READY=1, so it may run under the Type=notify unit;
	// `--print-storage-levels` prints the storage level vector this build
	// serves, as JSON, from the manifests of the modules serve hands app.New
	// (05-storage §5S.3.7). The flags are core's: app.FastExit answers them.
	// Handled before cobra for the same stdout reason as
	// --print-storage-format.
	if handled, err := app.FastExit(os.Args, os.Stdout, books.Module()); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	// Release tooling: `--version-json` (the updater's self-test, the deploy
	// post-check) and `--print-upgrade-floor`. Handled before cobra for the
	// same stdout reason, and before any store or config opens.
	if handled, err := fastExit(os.Args, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
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
