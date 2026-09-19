// file: internal/pathutil/root.go
// version: 1.0.0
// guid: b80aa1d4-2163-4b47-b76d-27c524672a3c
// last-edited: 2026-09-19

package pathutil

import (
	"errors"
	"path"
	"strings"
)

// SplitRoot splits p into the most specific named root that contains it and
// the path relative to that root. It is the wire format of the remote
// fingerprint worker API (windowed-fingerprint design (d2)): a job names a root
// ("libroot") and a relative path, and each side joins the relative path onto
// its OWN mount of that root, so no prefix string is ever rewritten.
//
// The match uses Abbreviate's separator boundary (p == root, or p starts with
// root + "/"), so a sibling whose name merely extends a root ("/lib-old" next
// to "/lib") never matches it. vars are checked in order, most specific first,
// exactly as PathVars returns them. ok is false when p is under no root, when p
// IS a root (there is no file to name), or when the relative part fails
// ValidateRel: a path that is not already clean ("a/../b", "a//b") is refused
// rather than cleaned, since cleaning it would name a different file than the
// server stats.
func SplitRoot(p string, vars []PathVar) (rootID, rel string, ok bool) {
	for _, v := range vars {
		if v.Value == "" {
			continue
		}
		if p == v.Value {
			return "", "", false
		}
		if strings.HasPrefix(p, v.Value+"/") {
			rel = p[len(v.Value)+1:]
			if ValidateRel(rel) != nil {
				return "", "", false
			}
			return v.Name, rel, true
		}
	}
	return "", "", false
}

// ErrBadRelPath is returned by ValidateRel.
var ErrBadRelPath = errors.New("pathutil: invalid relative path")

// ValidateRel checks a root-relative wire path: non-empty, POSIX-relative (no
// leading "/"), already clean (path.Clean(rel) == rel, so no "." or ".."
// segment, no doubled or trailing slash), never ".." or under "../", and free
// of NUL. It does NOT require valid UTF-8 and does NOT normalize Unicode: a
// ZFS filename is bytes, and an NFC and an NFD spelling are two different
// files that must each stay themselves.
func ValidateRel(rel string) error {
	switch {
	case rel == "":
		return errors.Join(ErrBadRelPath, errors.New("empty"))
	case strings.IndexByte(rel, 0) >= 0:
		return errors.Join(ErrBadRelPath, errors.New("contains NUL"))
	case strings.HasPrefix(rel, "/"):
		return errors.Join(ErrBadRelPath, errors.New("absolute"))
	case rel == ".." || strings.HasPrefix(rel, "../"):
		return errors.Join(ErrBadRelPath, errors.New("escapes the root"))
	case path.Clean(rel) != rel || rel == ".":
		return errors.Join(ErrBadRelPath, errors.New("not clean"))
	}
	return nil
}
