// file: internal/itunes/backupname.go
// version: 1.0.0
// guid: c81f2a47-6b30-4d95-a1e7-3f92c60b8d5a
// last-edited: 2026-09-01

package itunes

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup naming and rotation for <path>.bak-* files — one implementation.
//
// # The bug this replaces
//
// Three writers dropped backups of the SAME iTunes library into the SAME
// directory under the same ".bak-" prefix, using three different timestamp
// formats:
//
//	internal/itunes/itl_safe_write.go          2026-09-01T07-14-49.000000000Z
//	internal/itunes/service/writeback_batcher  20260801-000000   (and LOCAL time)
//	internal/itunes/service/transfer.go        20260701T000000Z
//
// Two independent rotators (rotateBackups and pruneITLBackups) each sorted the
// whole set with sort.Strings and deleted from the front, on a comment that
// said lexical order equals chronological order. That was true of each
// rotator's OWN format and false across the three, because the separators
// differ at the fifth character: '-' (0x2D) sorts before every digit, so every
// dashed-RFC3339 name from itl_safe_write sorts as older than every compact
// name — regardless of when it was actually written.
//
// Sorted lexically, a September backup and two summer ones come out:
//
//	iTunes Library.itl.bak-2026-09-01T07-14-49.000000000Z   <- actually NEWEST
//	iTunes Library.itl.bak-20260701T000000Z
//	iTunes Library.itl.bak-20260801-000000
//
// so a rotator keeping the newest deletes the September one and keeps July.
// The backups being destroyed first were the ones written by the hardened
// safe-write path, which is the only writer that fsyncs and pins a
// last-known-good anchor.
//
// # The fix
//
// Order by PARSED time, never by string. ParseBackupTime understands all three
// historical layouts, so backups already on disk sort correctly without any
// migration; new backups are written in one canonical layout.
//
// A name that parses under NO layout is kept, never rotated away. Deleting a
// file whose age cannot be established is how a rotation bug turns into data
// loss, and this whole file exists because that already happened once.

const (
	// BackupTimeLayout is the one layout new backups are written in. RFC3339
	// with ':' replaced, because ':' is legal on the Linux/ZFS production
	// target but hostile on other filesystems and in test fixtures.
	//
	// The zone is spelled Z0700, not the Z07-00 this constant carried before.
	// Z07-00 is not a zone specifier Go recognises as a unit: it matches Z07
	// and then treats "-00" as a LITERAL, so every backup the safe-write path
	// has ever produced is named "...Z-00". It round-tripped against itself,
	// so nothing noticed for as long as the only consumer sorted the names as
	// strings. Z0700 emits a bare "Z" for UTC, and the old spelling is kept
	// below as a parse-only layout so those names still date correctly.
	BackupTimeLayout = "2006-01-02T15-04-05.000000000Z0700"

	// BackupPrefix separates the library's own name from the stamp.
	BackupPrefix = ".bak-"

	// PinnedBackupName is the suffix of the pinned last-known-good anchor. It
	// carries no timestamp and is never rotated.
	PinnedBackupName = ".bak-lkg"
)

// legacyBackupLayouts are the two formats written before this file existed.
// They are parse-only: nothing writes them any more, but production has years
// of them on disk and mis-dating one is exactly the failure being fixed.
//
// The compact dashed form was written with time.Now() — LOCAL time, not UTC —
// so it must be parsed in the local zone. Parsing it as UTC would shift it by
// the machine's offset and can reorder backups taken hours apart.
var legacyBackupLayouts = []struct {
	layout string
	loc    *time.Location
}{
	// The safe-write path's own historical spelling — see BackupTimeLayout.
	// Every hardened backup currently on disk is named this way.
	{"2006-01-02T15-04-05.000000000Z07-00", time.UTC},
	{"20060102T150405Z", time.UTC},  // internal/itunes/service/transfer.go
	{"20060102-150405", time.Local}, // internal/itunes/service/writeback_batcher.go
}

// BackupName returns the full path of the backup of path taken at t.
func BackupName(path string, t time.Time) string {
	return path + BackupPrefix + t.UTC().Format(BackupTimeLayout)
}

// ParseBackupTime extracts the timestamp from a backup file's BASE NAME, given
// the base name of the library it backs up. ok is false for the pinned anchor,
// for a name that is not a backup of this library, and for any stamp that
// matches no known layout.
func ParseBackupTime(name, base string) (time.Time, bool) {
	if name == base+PinnedBackupName {
		return time.Time{}, false
	}
	stamp, found := strings.CutPrefix(name, base+BackupPrefix)
	if !found || stamp == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(BackupTimeLayout, stamp); err == nil {
		return t, true
	}
	for _, l := range legacyBackupLayouts {
		if t, err := time.ParseInLocation(l.layout, stamp, l.loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// BackupFile is one <path>.bak-* file found beside the library.
type BackupFile struct {
	// Name is the base name; Path is the full path.
	Name string
	Path string
	// Time is when the backup was taken, and Dated says whether it could be
	// established at all. An undated backup is never rotated away.
	Time  time.Time
	Dated bool
}

// ListBackups returns every <path>.bak-* file beside path, NEWEST FIRST,
// excluding the pinned anchor. Undated backups sort last so that any
// "keep the N newest" rule keeps the ones whose age is known.
func ListBackups(path string) ([]BackupFile, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("itunes: listing backups in %s: %w", dir, err)
	}

	var out []BackupFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == base+PinnedBackupName || !strings.HasPrefix(name, base+BackupPrefix) {
			continue
		}
		t, dated := ParseBackupTime(name, base)
		out = append(out, BackupFile{
			Name: name, Path: filepath.Join(dir, name), Time: t, Dated: dated,
		})
	}

	// Newest first, undated last. SliceStable, not Slice: sort.Slice is
	// UNSTABLE, and two backups can share a stamp (the compact layout has
	// one-second granularity). An arbitrary order between them would make
	// which one gets deleted depend on directory-read order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dated != out[j].Dated {
			return out[i].Dated
		}
		if !out[i].Dated {
			return false
		}
		return out[i].Time.After(out[j].Time)
	})
	return out, nil
}

// RotateBackups keeps the `keep` newest DATED backups of path and removes the
// rest. The pinned anchor and any undated backup are never removed.
//
// keep <= 0 is a no-op rather than "delete everything": callers pass a
// configured retention, and a zero that arrived from an unset config value
// must not wipe the library's backup history. (See the
// chapter_consolidation_threshold_min incident — a transient zero from a
// full-struct marshal became a permanent silent kill switch.)
func RotateBackups(path string, keep int) error {
	if keep <= 0 {
		return nil
	}
	backups, err := ListBackups(path)
	if err != nil {
		return err
	}

	var dated []BackupFile
	for _, b := range backups {
		if b.Dated {
			dated = append(dated, b)
		}
	}
	if len(dated) <= keep {
		return nil
	}

	var firstErr error
	for _, b := range dated[keep:] { // ListBackups is newest-first
		if rmErr := os.Remove(b.Path); rmErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("itunes: rotating backup %s: %w", b.Path, rmErr)
		}
	}
	return firstErr
}
