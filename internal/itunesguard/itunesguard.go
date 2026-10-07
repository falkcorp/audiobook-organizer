// file: internal/itunesguard/itunesguard.go
// version: 1.1.0
// guid: 1de95145-912c-41bf-badc-055bca21b429
// last-edited: 2026-10-06

// Package itunesguard is the one iTunes-ownership predicate for writes to a
// book's version-group primary flag. HARD RULE: an iTunes book's
// is_primary_version is never written. The Repairs merge fixers
// (internal/plugins/maintenance) and the operation revert
// (internal/audiobooks) both hand MayWrite to versionprimary (Env.MayWrite,
// Crown's Env), which asks it under the group lock about exactly the members
// it is about to write. It lives in its own package so both can import it:
// versionprimary cannot (repairs, which the path guard needs, imports
// versionprimary).
package itunesguard

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// Row is the iTunes-relevant part of one book_file row.
type Row struct {
	ID         string
	ITunesPID  string
	ITunesPath string
}

// OwnershipWhy says why book id is an iTunes book ("" when it is not). In
// order: a book iTunes persistent id (pid); a row iTunes persistent id, or
// (countPathRef) a row iTunes path reference; an un-tombstoned itunes
// external id; a path inside an "iTunes Media" folder; a path under
// books/itunes/** (symlinks resolved through res). doubt: a path could not be
// settled, so it cannot be told.
//
// countPathRef is false only for a book the consolidation-leftovers fixer
// retires (owner decision 2026-10-06).
func OwnershipWhy(res *repairs.PathResolver, id, pid string, paths []string, rows []Row, exts []database.ExternalIDMapping, countPathRef bool) (why string, doubt bool) {
	if pid != "" {
		return "book iTunes id " + pid, false
	}
	for _, r := range rows {
		switch {
		case r.ITunesPID != "":
			return "row iTunes id " + r.ITunesPID, false
		case countPathRef && r.ITunesPath != "":
			return "row iTunes path " + r.ITunesPath, false
		}
	}
	for _, e := range exts {
		if e.Source == "itunes" && e.ExternalID != "" && !e.Tombstoned {
			return "itunes external id " + e.ExternalID, false
		}
	}
	for _, p := range paths {
		if strings.Contains(p, string(filepath.Separator)+"iTunes Media"+string(filepath.Separator)) {
			return "file inside an iTunes Media folder: " + p, false
		}
	}
	switch k, w := repairs.GuardBookPathsWith(res, id, paths, ""); k {
	case repairs.SkipITunes:
		return w, false
	case repairs.SkipGuardUnreadable:
		return "", true
	}
	return "", false
}

// MemberStore is the store surface MemberWhy reads.
type MemberStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
}

// MemberWhy says why version-group (gid) member b's primary flag must not be
// written ("" when it may): it is an iTunes copy (OwnershipWhy, row path
// references counted), or that cannot be told.
func MemberWhy(store MemberStore, res *repairs.PathResolver, gid string, b *database.Book) (string, error) {
	rows, err := store.GetBookFiles(b.ID)
	if err != nil {
		return "", fmt.Errorf("files of %s: %w", b.ID, err)
	}
	exts, err := store.GetExternalIDsForBook(b.ID)
	if err != nil {
		return "", fmt.Errorf("external ids of %s: %w", b.ID, err)
	}
	paths := []string{b.FilePath}
	rs := make([]Row, 0, len(rows))
	for _, r := range rows {
		paths = append(paths, r.FilePath)
		rs = append(rs, Row{ID: r.ID, ITunesPID: r.ITunesPersistentID, ITunesPath: r.ITunesPath})
	}
	pid := ""
	if b.ITunesPersistentID != nil {
		pid = *b.ITunesPersistentID
	}
	why, doubt := OwnershipWhy(res, b.ID, pid, paths, rs, exts, true)
	if doubt {
		return fmt.Sprintf("could not tell whether version-group member %s is an iTunes copy", b.ID), nil
	}
	if why != "" {
		return fmt.Sprintf("iTunes copy %s in version group %s is not explicitly non-primary: the hand-off would write it", b.ID, gid), nil
	}
	return "", nil
}

// ErrITunesMember is wrapped by MayWrite's refusal. It wraps
// versionprimary.ErrWriteRefused, so the hand-off reports it as a refusal
// (nothing written, never retried as a failure).
var ErrITunesMember = fmt.Errorf("an iTunes book's primary flag is never written: %w", versionprimary.ErrWriteRefused)

// MayWrite is the versionprimary Env.MayWrite rule for group gid: it refuses
// (ErrITunesMember, with MemberWhy's reason) every member MemberWhy names. A
// failed read is returned as is, NOT as a refusal: the hand-off then fails
// without ErrWriteRefused and its caller retries it, so a transient store
// error is never recorded as "this member is an iTunes book". Each call
// shares one path resolver, so it is for one hand-off at a time, never
// concurrent use.
func MayWrite(store MemberStore, gid string) func(*database.Book) error {
	res := repairs.NewPathResolver()
	return func(m *database.Book) error {
		why, err := MemberWhy(store, res, gid, m)
		if err != nil {
			return err
		}
		if why != "" {
			return fmt.Errorf("%w: %s", ErrITunesMember, why)
		}
		return nil
	}
}
