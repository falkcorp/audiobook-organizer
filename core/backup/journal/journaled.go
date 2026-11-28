// file: core/backup/journal/journaled.go
// version: 0.1.0
// guid: 26b93f66-5734-4f16-b683-9985ba3754df
// last-edited: 2026-11-21

package journal

import (
	"errors"
	"os"
	"slices"
)

// Journaled reports whether the bound journal file already holds a verified
// line equal to r in Kind, Subject, Object and Detail: how a hook that journals
// before it acts (§4.12 record-before-act) tells, after a crash between the line
// and its commit, that a line is already durable and must not be appended a
// second time. At, Epoch, RestoreSeq, Source and the line's seq do not take part:
// they differ between the first attempt and the resumed one, and a caller that
// needs one line per run puts the run's key in Detail.
//
// A journal not bound yet, or with no file, holds no line. A file that exists and
// cannot be read is an error: answering "no" for it would append a duplicate to a
// journal that may hold the line.
func (c *Component) Journaled(r Revocation) (bool, error) {
	path := c.Path()
	if path == "" {
		return false, nil
	}
	chain, err := ReadFile(path, c.o.Keys, WithFS(c.o.FS))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}
	return chain.Has(0, func(l Line) bool {
		return l.Kind == r.Kind && l.Subject == r.Subject && l.Object == r.Object && slices.Equal(l.Detail, r.Detail)
	}), nil
}
