// file: core/library/closelog.go
// version: 1.0.0
// guid: 76865a86-24e2-4dd6-a3bf-23fcc26d6c67
// last-edited: 2026-11-11

package library

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// closeLogged closes c and logs a failure. It is for a handle that was only read,
// or only used to name a directory for an operation whose outcome is already
// decided: a close error then changes no result, but it is evidence about the
// disk and must not vanish. A handle a write went through is closed and checked
// in line instead, because there a failed close means the bytes may not be there.
func closeLogged(log *slog.Logger, c io.Closer, what string) {
	if err := c.Close(); err != nil {
		if log == nil {
			log = slog.Default()
		}
		log.Warn("library: could not close a handle", "what", what, "err", err)
	}
}

// withRollback is err with a failed undo attached, or err itself, unchanged, when
// the undo (rerr) worked. The undo is what leaves nothing behind after a failure;
// one that fails too leaves a stray the caller must hear about.
func withRollback(err error, what string, rerr error) error {
	if rerr == nil {
		return err
	}
	return errors.Join(err, fmt.Errorf("library: could not undo %s: %w", what, rerr))
}
