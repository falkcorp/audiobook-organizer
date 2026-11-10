// file: internal/testutil/rapidgen/rapidgen.go
// version: 1.0.3
// guid: b093c713-a945-4937-8871-a7786dd843cb

// Package rapidgen provides reusable `pgregory.net/rapid` generators for the
// project's core domain types. All property-based tests across the codebase
// should draw their fuzz inputs from here so invariants are exercised with the
// same distribution of shapes — random titles, optional-pointer permutations,
// valid-but-weird statuses — regardless of which package the test lives in.
//
// This is a regular (non-_test) package so it can be imported from tests in
// any package (database, server, search, auth). Importing a `_test.go` file
// across packages is not supported in Go; the small cost of pulling rapid
// into the production binary is the trade-off.
package rapidgen

import (
	"fmt"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"pgregory.net/rapid"
)

// ----------------------------------------------------------------------------
// Primitive helpers
// ----------------------------------------------------------------------------

// nonEmptyString draws a string guaranteed to contain at least one non-space
// rune. rapid's default string generator includes the empty string and
// whitespace-only strings, which trip over validation code that calls
// strings.TrimSpace before comparing to "".
func nonEmptyString(t *rapid.T, label string) string {
	return rapid.StringMatching(`[A-Za-z0-9][A-Za-z0-9 \-_'.,:!?]{0,63}`).Draw(t, label)
}

// alnum draws lowercase alphanumeric strings of the given length range. Used
// for usernames and other identifier-shaped fields.
func alnum(t *rapid.T, label string, minLen, maxLen int) string {
	return rapid.StringMatching(
		fmt.Sprintf(`[a-z0-9]{%d,%d}`, minLen, maxLen),
	).Draw(t, label)
}

// optString returns a *string that is nil ~30% of the time and a non-empty
// string otherwise. Using a skewed distribution (not 50/50) is deliberate —
// most real data has optional fields populated, but we still want to exercise
// the nil path.
func optString(t *rapid.T, label string) *string {
	if rapid.Float64Range(0, 1).Draw(t, label+"_present") < 0.7 {
		s := nonEmptyString(t, label)
		return &s
	}
	return nil
}

// optInt returns a *int that is nil ~30% of the time.
func optInt(t *rapid.T, label string, min, max int) *int {
	if rapid.Float64Range(0, 1).Draw(t, label+"_present") < 0.7 {
		v := rapid.IntRange(min, max).Draw(t, label)
		return &v
	}
	return nil
}

// ----------------------------------------------------------------------------
// Core type generators
// ----------------------------------------------------------------------------

// Book generates a *database.Book with a random non-empty Title, optional
// author/series metadata, and a plausible file path. The ID is left empty so
// CreateBook assigns a fresh ULID — round-trip tests can pass the result of
// CreateBook back to GetBookByID without collision.
func Book(t *rapid.T) *database.Book {
	format := rapid.SampledFrom([]string{"m4b", "mp3", "flac", "ogg", "m4a"}).Draw(t, "format")
	return &database.Book{
		Title:                nonEmptyString(t, "title"),
		FilePath:             "/library/" + alnum(t, "pathseg", 4, 16) + "/" + alnum(t, "file", 4, 16) + "." + format,
		Format:               format,
		Duration:             optInt(t, "duration", 60, 360000), // 1min–100h
		Narrator:             optString(t, "narrator"),
		Description:          optString(t, "description"),
		Language:             optString(t, "language"),
		Publisher:            optString(t, "publisher"),
		Genre:                optString(t, "genre"),
		PrintYear:            optInt(t, "print_year", 1800, 2030),
		AudiobookReleaseYear: optInt(t, "release_year", 1950, 2030),
		ISBN10:               optString(t, "isbn10"),
		ISBN13:               optString(t, "isbn13"),
		ASIN:                 optString(t, "asin"),
	}
}

// Author generates a random database.Author. ID is left 0 so CreateAuthor
// assigns a fresh integer ID.
func Author(t *rapid.T) database.Author {
	return database.Author{
		Name: nonEmptyString(t, "author_name"),
	}
}

// Series generates a random database.Series. ID is left 0 so CreateSeries
// assigns a fresh integer ID. AuthorID is always nil — callers that want to
// tie the series to an author should set it after generation.
func Series(t *rapid.T) database.Series {
	return database.Series{
		Name: nonEmptyString(t, "series_name"),
	}
}

// BookFile generates a *database.BookFile scoped to the given bookID. ID is
// left empty so CreateBookFile / UpsertBookFile assigns a fresh ULID.
func BookFile(t *rapid.T, bookID string) database.BookFile {
	format := rapid.SampledFrom([]string{"m4b", "mp3", "flac", "m4a", "ogg"}).Draw(t, "bf_format")
	track := rapid.IntRange(1, 40).Draw(t, "bf_track")
	return database.BookFile{
		BookID:      bookID,
		FilePath:    "/library/" + bookID + "/" + alnum(t, "bf_file", 4, 16) + "." + format,
		Format:      format,
		Codec:       rapid.SampledFrom([]string{"aac", "mp3", "flac", "opus"}).Draw(t, "bf_codec"),
		Duration:    database.Secs(rapid.IntRange(60, 36000).Draw(t, "bf_duration")), // always known; a later PR also draws unknown and failed states
		FileSize:    rapid.Int64Range(1024, 2<<30).Draw(t, "bf_size"),
		TrackNumber: track,
		TrackCount:  rapid.IntRange(track, track+20).Draw(t, "bf_trackcount"),
	}
}

// bookVersionStatuses is the subset of version statuses that generators emit.
// Transition-only statuses (swapping_in, swapping_out) are created by the
// server layer during lifecycle operations, never by direct CreateBookVersion
// calls, so we exclude them here.
var bookVersionStatuses = []string{
	database.BookVersionStatusPending,
	database.BookVersionStatusActive,
	database.BookVersionStatusAlt,
	database.BookVersionStatusTrash,
	database.BookVersionStatusInactivePurged,
	database.BookVersionStatusBlockedForRedownload,
}

// BookVersion generates a *database.BookVersion scoped to the given bookID.
// ID is left empty so CreateBookVersion assigns a fresh ULID. Status is drawn
// from the set of statuses that are valid as direct-create inputs.
func BookVersion(t *rapid.T, bookID string) *database.BookVersion {
	return &database.BookVersion{
		BookID:      bookID,
		Status:      rapid.SampledFrom(bookVersionStatuses).Draw(t, "bv_status"),
		Format:      rapid.SampledFrom([]string{"m4b", "mp3", "flac", "ogg"}).Draw(t, "bv_format"),
		Source:      rapid.SampledFrom([]string{"deluge", "manual", "transcoded", "imported"}).Draw(t, "bv_source"),
		TorrentHash: alnum(t, "bv_hash", 0, 40),
		IngestDate:  time.Now().Add(-time.Duration(rapid.IntRange(0, 365*86400).Draw(t, "bv_age")) * time.Second),
	}
}

// BookVersionActive generates a BookVersion with status forced to "active".
// Useful for the single-active-invariant test where we explicitly want one
// active row per book.
func BookVersionActive(t *rapid.T, bookID string) *database.BookVersion {
	v := BookVersion(t, bookID)
	v.Status = database.BookVersionStatusActive
	return v
}

// User generates the tuple (username, email, passwordHash) accepted by
// Store.CreateUser. The caller supplies passwordHashAlgo, roles, and status.
// Usernames are lowercase alphanumeric 3–24 chars; emails contain an @ with
// non-empty parts on both sides.
func User(t *rapid.T) (username, email, passwordHash string) {
	username = alnum(t, "username", 3, 24)
	emailLocal := alnum(t, "email_local", 3, 16)
	emailDomain := alnum(t, "email_domain", 3, 12)
	emailTLD := rapid.SampledFrom([]string{"com", "net", "org", "io", "dev"}).Draw(t, "email_tld")
	email = fmt.Sprintf("%s@%s.%s", emailLocal, emailDomain, emailTLD)
	// 32 hex chars — looks like a hash, no collisions with other draws.
	passwordHash = rapid.StringMatching(`[a-f0-9]{32,64}`).Draw(t, "password_hash")
	return
}

// UserPlaylist generates a *database.UserPlaylist of type "static" or "smart".
// For static playlists BookIDs is populated (possibly empty slice); for smart
// playlists Query is populated with a non-empty DSL-shaped string.
func UserPlaylist(t *rapid.T) *database.UserPlaylist {
	plType := rapid.SampledFrom([]string{
		database.UserPlaylistTypeStatic,
		database.UserPlaylistTypeSmart,
	}).Draw(t, "upl_type")

	pl := &database.UserPlaylist{
		Name:        nonEmptyString(t, "upl_name"),
		Description: rapid.StringMatching(`[A-Za-z0-9 ]{0,80}`).Draw(t, "upl_desc"),
		Type:        plType,
	}

	if plType == database.UserPlaylistTypeStatic {
		n := rapid.IntRange(0, 8).Draw(t, "upl_bookcount")
		pl.BookIDs = make([]string, n)
		for i := range pl.BookIDs {
			pl.BookIDs[i] = alnum(t, fmt.Sprintf("upl_bookid_%d", i), 16, 26)
		}
	} else {
		// Smart playlist: a simple DSL query like `author:"Foo"`.
		pl.Query = fmt.Sprintf("%s:%s",
			rapid.SampledFrom([]string{"author", "series", "format", "genre"}).Draw(t, "upl_field"),
			alnum(t, "upl_value", 2, 12))
		pl.Limit = rapid.IntRange(0, 500).Draw(t, "upl_limit")
	}
	return pl
}

// Tag generates a lowercase tag string 2–20 characters long. Matches the
// canonical user-tag shape enforced by the UI (lowercased, alnum + hyphen).
func Tag(t *rapid.T) string {
	return rapid.StringMatching(`[a-z][a-z0-9-]{1,19}`).Draw(t, "tag")
}

// operationChangeTypes enumerates the change_type values tracked by the undo
// engine. Keep this in sync with the comment on database.OperationChange.
var operationChangeTypes = []string{"file_move", "metadata_update", "tag_write"}

// OperationChange generates a *database.OperationChange for the given
// operationID and bookID. CreatedAt is set to the current time; RevertedAt is
// left nil (revert is an explicit transition the test applies after creation).
func OperationChange(t *rapid.T, operationID, bookID string) *database.OperationChange {
	changeType := rapid.SampledFrom(operationChangeTypes).Draw(t, "oc_type")
	var fieldName string
	switch changeType {
	case "file_move":
		fieldName = "file_path"
	case "metadata_update":
		fieldName = rapid.SampledFrom([]string{"title", "narrator", "genre", "isbn13"}).Draw(t, "oc_field")
	case "tag_write":
		fieldName = rapid.SampledFrom([]string{"TITLE", "ARTIST", "GENRE", "AUDIOBOOK_ORGANIZER_BOOK_ID"}).Draw(t, "oc_tag")
	}
	return &database.OperationChange{
		OperationID: operationID,
		BookID:      bookID,
		ChangeType:  changeType,
		FieldName:   fieldName,
		OldValue:    nonEmptyString(t, "oc_old"),
		NewValue:    nonEmptyString(t, "oc_new"),
		CreatedAt:   time.Now(),
	}
}
