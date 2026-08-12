// file: internal/syncapi/conformance/normalize.go
// version: 1.2.0
// guid: ed9387e2-9e6a-46d7-912a-28c96d442d0d
// last-edited: 2026-08-12

package conformance

import "strings"

// volatilePlaceholder replaces volatile string values. Values are canonicalized
// rather than removed so that JSON *type* survives normalization -- the differ
// compares types, so erasing them would defeat the whole harness.
const volatilePlaceholder = "<volatile>"

// DefaultVolatileKeys returns the lowercased field names whose values differ
// between two runs of the same request and therefore carry no conformance
// signal: identifiers, timestamps, inodes, and secrets.
//
// Deliberately NOT volatile: currentTime, duration, progress, startOffset,
// isFinished -- these are real playback/progress data whose values matter.
func DefaultVolatileKeys() map[string]bool {
	keys := []string{
		// identifiers
		"id", "libraryid", "libraryitemid", "folderid", "userid", "sessionid",
		"episodeid", "ino", "authorid", "seriesid", "collectionid",
		// mediaitemid/bookid are the same minted sync ids as libraryitemid, reached
		// by a different name from the progress and playback bodies. They were left
		// off until 2026-08-12, so turning value comparison on reported four
		// "mismatches" that no run could ever satisfy.
		"mediaitemid", "bookid",
		// secrets / tokens
		"token", "refreshtoken", "accesstoken", "apikey", "password",
		// timestamps
		"createdat", "updatedat", "addedat", "lastupdate", "lastseen",
		"startedat", "finishedat", "birthtimems", "mtimems", "ctimems",
		"scanversion", "lastscan", "loadedat",
		// A listening session records WHEN it happened. The capture's Thursday is not
		// ours and never will be. Note these are the session's own date fields; the
		// book's publication date is publishedDate/tagDate and stays compared.
		"date", "dayofweek",
		// host-dependent
		"path", "relpath", "contenturl", "coverpath", "metadatapath",
		"fullpath",
		// request-dependent: describe the CALLER, not the response format.
		// A capture taken on the oracle's machine can never match ours, and
		// nothing about ABS compatibility depends on reproducing its client.
		// Note these are populated for real -- me.go:127 maps s.UserAgent --
		// so normalizing them hides no gap; the tests just don't set headers.
		"ipaddress", "useragent",
	}
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

// Normalizer canonicalizes volatile values so that two captures of the same
// endpoint compare equal.
type Normalizer struct {
	Volatile map[string]bool
}

// NewNormalizer returns a Normalizer using DefaultVolatileKeys.
func NewNormalizer() *Normalizer {
	return &Normalizer{Volatile: DefaultVolatileKeys()}
}

// Normalize returns a deep copy of v with volatile values canonicalized. The
// input is never mutated.
func (n *Normalizer) Normalize(v any) any {
	return n.normalize(v, false)
}

// normalize deep-copies v. When volatile is true, scalar values are replaced
// with a canonical value of the SAME JSON type.
func (n *Normalizer) normalize(v any, volatile bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = n.normalize(child, n.isVolatile(k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			// Array elements inherit the parent key's volatility so that
			// e.g. "tags": ["a","b"] under a volatile key is canonicalized.
			out[i] = n.normalize(child, volatile)
		}
		return out
	case string:
		if volatile {
			return volatilePlaceholder
		}
		return t
	case float64:
		if volatile {
			return float64(0)
		}
		return t
	case bool:
		if volatile {
			return false
		}
		return t
	default:
		// nil and unknown types pass through; nil already carries no value.
		return v
	}
}

func (n *Normalizer) isVolatile(key string) bool {
	return n.Volatile[strings.ToLower(key)]
}
