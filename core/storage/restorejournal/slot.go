// file: core/storage/restorejournal/slot.go
// version: 1.0.0
// guid: 9a2d6e41-3f7c-4b18-8e05-c4b1a7d93f26
// last-edited: 2026-11-08

package restorejournal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// The reader check's tokens (05-storage §5S.3.7; 05-updater §5U.12): a
// swap staged in the one slot, and a swap in flight.
const (
	TokenRestorePending  = "restore_pending"
	TokenRestoreInflight = "restore_inflight"
)

// SlotEntry is one swap the slot names: its intent's id, kind and reset
// kind. A file that does not decode leaves them empty and still counts.
type SlotEntry struct {
	ID    string
	Kind  Kind
	Reset ResetKind
}

func (e SlotEntry) String() string {
	kind := string(e.Kind)
	if kind == "" {
		kind = "unreadable"
	}
	if e.Reset != "" {
		kind += " (" + string(e.Reset) + ")"
	}
	if e.ID == "" {
		return kind + " intent"
	}
	return kind + " intent " + e.ID
}

// SlotState is what the one slot holds, read without acting on it: the
// probe of every opener without P0a (05-storage §5S.4 "Offline open" rule
// 1, the updater's apply, ABO's standalone opener). The kernel's own open
// runs P0a instead.
type SlotState struct {
	Pending  *SlotEntry  // restore/pending.json
	InFlight []SlotEntry // each restore/<id>/inflight.json, by id
}

// Busy reports a staged or in-flight swap.
func (s SlotState) Busy() bool { return s.Pending != nil || len(s.InFlight) > 0 }

// Err is nil for an empty slot, else ErrSwapPending naming the token
// (restore_inflight before restore_pending, since an in-flight swap is the
// one that must finish first), the kind and the id, and what to do.
func (s SlotState) Err() error {
	switch {
	case len(s.InFlight) > 0:
		names := make([]string, len(s.InFlight))
		for i, e := range s.InFlight {
			names[i] = e.String()
		}
		return fmt.Errorf("%w (%s: %s); boot the current build once to finish it", ErrSwapPending, TokenRestoreInflight, strings.Join(names, ", "))
	case s.Pending != nil:
		return fmt.Errorf("%w (%s: %s); it applies at the next start of the service, or the next start of another build rejects it", ErrSwapPending, TokenRestorePending, s.Pending)
	}
	return nil
}

// ReadSlot reads the slot under stateDir through fs: pending.json, and the
// intent of each in-flight swap. An absent restore/ is an empty slot; an
// error reading a file that exists is returned, so a caller that refuses on
// a busy slot fails closed.
func ReadSlot(fs vfs.FS, stateDir string) (SlotState, error) {
	var st SlotState
	e, ok, err := readEntry(fs, PendingPath(stateDir))
	if err != nil {
		return st, err
	}
	if ok {
		st.Pending = &e
	}
	ids, err := InFlightIDs(fs, stateDir)
	if err != nil {
		return st, err
	}
	for _, id := range ids {
		e, ok, err := readEntry(fs, filepath.Join(JournalDir(stateDir, id), inflightName))
		if err != nil {
			return st, err
		}
		if ok {
			if e.ID == "" {
				e.ID = id
			}
			st.InFlight = append(st.InFlight, e)
		}
	}
	return st, nil
}

// readEntry reads one intent file's id and kinds; ok is false when it does
// not exist.
func readEntry(fs vfs.FS, path string) (SlotEntry, bool, error) {
	f, err := fs.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return SlotEntry{}, false, nil
	}
	if err != nil {
		return SlotEntry{}, false, fmt.Errorf("restorejournal: open %s: %w", path, err)
	}
	b, err := io.ReadAll(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return SlotEntry{}, false, fmt.Errorf("restorejournal: read %s: %w", path, err)
	}
	var in Intent
	if json.Unmarshal(b, &in) != nil {
		return SlotEntry{}, true, nil // occupied all the same
	}
	return SlotEntry{ID: in.ID, Kind: in.Kind, Reset: in.Reset}, true, nil
}
