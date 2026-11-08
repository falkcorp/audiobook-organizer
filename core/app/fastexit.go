// file: core/app/fastexit.go
// version: 1.0.0
// guid: b77f70ae-262e-4c32-83d0-514fc4d5b9be
// last-edited: 2026-11-08

package app

import (
	"fmt"
	"io"

	"github.com/falkcorp/mediacore/internal/sdnotify"
)

// printCapabilitiesFlag is the one argument of the fast exit that tells the
// deploy and rollback recipes what a binary supports.
const printCapabilitiesFlag = "--print-capabilities"

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
func FastExit(args []string, stdout io.Writer) (handled bool, err error) {
	if len(args) == 2 && args[1] == printCapabilitiesFlag {
		_, err = fmt.Fprintln(stdout, sdnotify.Capability)
		return true, err
	}
	return false, nil
}
