// file: internal/books/store/booksclaims.go
// version: 1.0.1
// guid: 9d3b6e15-2a7f-4c80-b4e9-6f1c0a8d2e57
// last-edited: 2026-11-09

package store

import (
	"strings"

	"github.com/falkcorp/mediacore/storage"
)

// BooksOwner is books' storage owner: its module ID, under which core storage
// records its claims, its applied versions (through BooksVersionSource) and
// its level.
const BooksOwner storage.Owner = "books"

// Books' claims (05-storage §5S.9.1, the ownership table).
//
// The claim list is DERIVED from keyFamilies at run time rather than
// generated into a checked-in file: keyFamilies is the single source, so
// there is no second copy that could drift, and adding a family to
// keyfamilies.go is the one edit that lets books write it. (The spec's
// wording is "generated into internal/books/storage_claims.go by a go
// generate tool"; the derivation lives here, in internal/books/store, because
// the standalone opener (CLI commands, tools, every package's tests) opens
// the store through NewPebbleStore and internal/books/store cannot import
// internal/books. internal/books re-exports it.)
//
// At release R1 every family is ClaimReadWrite (D-S3; the brief: "every
// family ClaimReadWrite"): HEAD's retired children (book:hash:,
// book:organizedhash:, book:originalhash:, library:) and aging-out children
// (book:author:, book:series:) are RW children of their RW parents, read and
// swept by books' code as at HEAD. R2 retires the cutover families.

// reservedFamily reports the core-reserved rows of keyFamilies (c/, m/):
// core's keyspaces, never a books claim (05-storage §5S.3.4 kernel
// validation item 2).
func reservedFamily(prefix string) bool { return prefix == "c/" || prefix == "m/" }

// claimSeparators are the bytes a claim prefix must end with (core's claim
// grammar); a registered family that ends in none of them is one bare key
// (narrator_counter, sync_alias_use_seed_cutoff) and is claimed by Key.
const claimSeparators = ":/_."

// exactKeyFamily reports a family that is one bare key.
func exactKeyFamily(prefix string) bool {
	return prefix != "" && !strings.ContainsRune(claimSeparators, rune(prefix[len(prefix)-1]))
}

// serializedFamilies are the registered families of the ownership table's
// rows 1–6 (users, roles, API keys, invites and the invite allow-list, OAuth
// identities, sessions): Serialized, so their writes take commit stripes and
// may carry an Expect. Storage refuses no blind write in a legacy claim
// (A-3), so marking them costs books no code change.
var serializedFamilies = map[string]bool{
	"u:":           true,
	"role:":        true,
	"apikey:":      true,
	"invite:":      true,
	"inviteallow:": true,
	"oauthid:":     true,
	"sess:":        true,
}

// serializedSubClaims are the ownership table's same-owner nested claims that
// keyFamilies does not register as families: the idx: sub-prefixes of rows
// 1–6 and row 7's exact settings keys (the config blob and HEAD's four
// legacy secret rows), all Serialized.
var serializedSubClaims = []storage.Claim{
	{Prefix: "idx:user:", Description: "user indexes (ownership table row 1)"},
	{Prefix: "idx:role:name:", Description: "role name index (row 2)"},
	{Prefix: "idx:apikey:", Description: "API key indexes (row 3)"},
	{Prefix: "idx:invite:username:", Description: "invite username index (row 4a)"},
	{Prefix: "idx:oauth:", Description: "OAuth identity indexes (row 5)"},
	{Prefix: "idx:sess:user:", Description: "session-by-user index (row 6)"},
	{Key: "setting:config_blob", Description: "the settings blob (row 7)"},
	{Key: "setting:openai_api_key", Description: "legacy secret row (row 7)"},
	{Key: "setting:google_books_api_key", Description: "legacy secret row (row 7)"},
	{Key: "setting:hardcover_api_token", Description: "legacy secret row (row 7)"},
	{Key: "setting:basic_auth_password", Description: "legacy secret row (row 7)"},
}

// BooksClaims is books' claim list for this build: one ClaimReadWrite claim
// per registered family (a Key claim for a bare-key family), the reserved
// c/ and m/ rows skipped, rows 1–7 Serialized, plus the nested Serialized
// claims keyFamilies does not register. A fresh slice on every call.
func BooksClaims() []storage.Claim {
	fams := KeyFamilies()
	out := make([]storage.Claim, 0, len(fams)+len(serializedSubClaims))
	for _, f := range fams {
		if reservedFamily(f.Prefix) {
			continue
		}
		c := storage.Claim{Mode: storage.ClaimReadWrite, Serialized: serializedFamilies[f.Prefix], Description: f.Description}
		if exactKeyFamily(f.Prefix) {
			c.Key = f.Prefix
		} else {
			c.Prefix = f.Prefix
		}
		out = append(out, c)
	}
	for _, c := range serializedSubClaims {
		c.Mode, c.Serialized = storage.ClaimReadWrite, true
		out = append(out, c)
	}
	return out
}
