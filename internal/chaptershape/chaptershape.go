// file: internal/chaptershape/chaptershape.go
// version: 1.1.0
// guid: 5d1f7c2a-8e43-4b69-a0d5-2c9e61b4f873
// last-edited: 2026-09-12

// Package chaptershape recognises the "one chapter per folder" layout:
//
//	<Book>/<Book> - 1/file
//	<Book>/<Book> - 2/file
//	...
//
// The scanner groups files per leaf directory, so this layout yields one
// single-file book row per chapter. Two callers need to agree on what the
// shape is: the scanner's coalesceShatteredSiblings (which merges the rows)
// and organize (which must not move such a chapter out of its folder: the
// folder name is the only place its chapter number lives, because a
// single-file row has no track and the default file pattern drops " - NN").
//
// The precision guard is the one maintenance.fs-regroup-xml validated in
// production: the chapter prefix must appear in the parent folder's name.
// That excludes flat dumps (`abooks/Throne of Jade 01/...`) and series volumes
// (`Author/Series - 3/file`).
package chaptershape

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// dirRe matches a chapter folder basename "<prefix> - <number>". Mirrors
// itunesservice.chapterDirRe.
var dirRe = regexp.MustCompile(`^(.*) - (\d+)$`)

// Parts returns the book folder (the chapter folder's parent), the book-title
// prefix and the chapter number for a file inside a "<prefix> - N" folder.
// ok is false when fp's folder does not have that shape. It does not apply
// the prefix-in-parent guard; see IsChapterFolderFile.
func Parts(fp string) (parent, prefix string, num int, ok bool) {
	chapterDir := filepath.Dir(fp)
	m := dirRe.FindStringSubmatch(filepath.Base(chapterDir))
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", "", 0, false
	}
	return filepath.Dir(chapterDir), strings.TrimSpace(m[1]), n, true
}

// NormPrefix lowercases and keeps only letters and digits, in any script.
//
// It deliberately differs from itunesservice.normTitle, which keeps only ASCII
// a-z0-9. With the ASCII rule every non-Latin title normalises to "", so
// PrefixInParent rejected `Сияние/Сияние - 1/58.MP3`: organize then moved each
// chapter of that book to one shared target and renamed the rest to `_copyN`,
// and the scanner stopped coalescing its chapters. Unicode letters and digits
// keep the guard's meaning (the book folder is named after the book) for every
// script. A prefix with no letters or digits at all still normalises to "".
func NormPrefix(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// PrefixInParent is the precision guard: the book folder must be named after
// the book. A prefix that normalises to nothing never passes.
func PrefixInParent(parent, prefix string) bool {
	p := NormPrefix(prefix)
	return p != "" && strings.Contains(NormPrefix(filepath.Base(parent)), p)
}

// IsChapterFolderFile reports whether fp is a file inside a chapter folder of
// a book laid out one chapter per folder: Parts matches AND the guard holds.
// It decides from the path alone, so the answer does not depend on which
// other books are being looked at in the same batch.
func IsChapterFolderFile(fp string) (parent, prefix string, ok bool) {
	parent, prefix, _, ok = Parts(fp)
	if !ok || !PrefixInParent(parent, prefix) {
		return "", "", false
	}
	return parent, prefix, true
}
