// file: internal/authorname/placeholder_title.go
// version: 1.0.0
// guid: 70b276f9-2e24-480c-8fc2-071fc018b71e
// last-edited: 2026-09-27

package authorname

import "strings"

// PlaceholderTitle is the title the organizer writes when a book has no
// resolvable title. Like Placeholder it ends up in directory and file names,
// so it round-trips back into the title field of any book re-imported from
// such a path.
const PlaceholderTitle = "Unknown Title"

// narratorPlaceholderTitles are titles that are really a narrator placeholder
// that leaked into the title field. "read by narrator" is the tail the old
// organizer default pattern wrote into filenames; maintenance's junk-title
// repair counted 1,595 such books.
var narratorPlaceholderTitles = map[string]struct{}{
	"narrator":         {},
	"read by narrator": {},
	"unknown narrator": {},
}

// IsPlaceholderTitle reports whether title is empty or one of the system's own
// placeholders rather than a real title.
//
// It lives here, not in internal/organizer where it started, because two
// packages that cannot import each other need the SAME answer:
//
//   - internal/organizer refuses to organize a placeholder-titled book, since
//     every such book computes the same destination path (the 2026-08-11
//     "848 books onto one path" collapse);
//   - internal/dedup refuses to pair two books on a placeholder title, since
//     every such book "matches" every other one (2026-09-27: the Dupes lane
//     was full of exact pairs titled "read by narrator" and "Unknown Title").
//
// A copy in each package would drift the first time someone adds a new
// placeholder to one of them.
func IsPlaceholderTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" || strings.EqualFold(t, PlaceholderTitle) || IsPlaceholder(t) {
		return true
	}
	_, ok := narratorPlaceholderTitles[strings.ToLower(t)]
	return ok
}

// IsPlaceholderAuthor reports whether an author NAME carries no identity: the
// empty string, the Placeholder itself, or any placeholder title that leaked
// into the author field.
//
// It is deliberately broader than IsPlaceholder. IsPlaceholder answers "is
// this the one literal the system writes", which the path parsers need to be
// exact about. Dedup asks a different question -- "does sharing this author
// tell us two books are the same work?" -- and production answered it: author
// rows named "read by narrator" (452 books) and "Unknown Title" (309 books)
// exist because the path parsers once read those segments as authors. Two
// books under such an author share nothing but the parsing accident.
func IsPlaceholderAuthor(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" || strings.EqualFold(n, "unknown") {
		return true
	}
	return IsPlaceholderTitle(n)
}
