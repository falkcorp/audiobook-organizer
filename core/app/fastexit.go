// file: core/app/fastexit.go
// version: 1.1.0
// guid: b77f70ae-262e-4c32-83d0-514fc4d5b9be
// last-edited: 2026-11-08

package app

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/falkcorp/mediacore/internal/sdnotify"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// The fast-exit flags: each is the one argument of the command line, and its
// answer tells the deploy and rollback recipes what a binary supports.
const (
	printCapabilitiesFlag  = "--print-capabilities"
	printStorageLevelsFlag = "--print-storage-levels"
)

// FastExit answers the flags a host binary must handle before it parses its
// own command line, because the answer is a fixed stdout and nothing else
// may be printed first (a command framework prints its own banner). args is
// os.Args. handled is true when args was exactly one such flag and its answer
// was written to stdout; the caller then exits without starting anything,
// with status 1 when err is not nil.
//
// --print-capabilities prints one token per line. Today the only token is
// sd_notify: this binary sends READY=1, so it may run under a Type=notify
// unit. The deploy and rollback recipes ask a binary for it before restarting
// it; a build that predates the flag fails with "unknown flag" and prints
// nothing to stdout, which they read as "no sd_notify" and run it as
// Type=simple instead of letting systemd kill it at TimeoutStartSec.
//
// --print-storage-levels prints the build's supported level vector (05-storage
// §5S.3.7 "Fast-exit flags") as one JSON object, owner to level, core's own
// owner included: computed from mods' manifests alone (storage.Supported, the
// vector the kernel's P2 compares a store against), before anything opens.
// mods are the modules the host passes to New, enabled or not: a disabled
// module's levels still bound what the build can read. make rollback asks the
// previous binary for it, and the image probe requires exit 0 and JSON.
func FastExit(args []string, stdout io.Writer, mods ...module.Module) (handled bool, err error) {
	if len(args) != 2 {
		return false, nil
	}
	switch args[1] {
	case printCapabilitiesFlag:
		_, err = fmt.Fprintln(stdout, sdnotify.Capability)
		return true, err
	case printStorageLevelsFlag:
		return true, printStorageLevels(stdout, mods)
	}
	return false, nil
}

// printStorageLevels writes storage.Supported over mods' storage owners as
// one line of JSON.
func printStorageLevels(stdout io.Writer, mods []module.Module) error {
	var owners []storage.OwnerSpec
	for _, m := range mods {
		man := m.Manifest()
		if kernel.DeclaresStorage(man) {
			owners = append(owners, storage.OwnerSpec{Owner: storage.Owner(man.ID), Spec: man.Storage})
		}
	}
	line, err := json.Marshal(storage.Supported(owners))
	if err != nil {
		return fmt.Errorf("app: %s: %w", printStorageLevelsFlag, err)
	}
	_, err = fmt.Fprintf(stdout, "%s\n", line)
	return err
}
