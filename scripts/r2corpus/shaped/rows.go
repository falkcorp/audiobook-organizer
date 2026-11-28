// file: scripts/r2corpus/shaped/rows.go
// version: 1.0.0
// guid: f846ac1d-7a69-4229-9307-6bbe75cbc88c
// last-edited: 2026-11-10

//go:build r2corpus_shaped

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
)

// minSubstituteLen is the shortest template key suffix whose quoted occurrences
// in the value are rewritten to a generated row's own suffix. A shorter suffix
// ("1") would rewrite unrelated values.
const minSubstituteLen = 6

// rowShape is what the generator may change in a family's cloned rows beyond
// the key suffix: the one free-text field it resizes, and the field every
// clone must hold its own SHA-256 of.
type rowShape struct {
	// pad is a top-level string field of free text (a message, a summary, a
	// description no step reads): the only place a value grows or shrinks.
	// Empty means the family's rows are cloned at their template's size. A
	// key, an ID, a hash, a timestamp, an enum or any other typed field is
	// never padded, and neither is a field of a family M3 drains (u:, role:,
	// apikey:, invite:, oauthid:): M3 checks or normalises those, so a padded
	// one is a cutover blocker or a recorded change the corpus never had.
	pad string
	// hash is a top-level field holding the hex SHA-256 of a secret that M3
	// requires to be distinct per row (an API key's token_hash): each clone
	// holds a SHA-256 of its own key suffix, so no two rows share one.
	hash string
}

// rowShapes are the families whose clones change beyond the key suffix. Every
// other family's rows are cloned verbatim but for the suffix.
var rowShapes = map[string]rowShape{
	"act:change:": {pad: "summary"},
	"act:debug:":  {pad: "summary"},
	"act:info:":   {pad: "summary"},
	"opv2:def:":   {pad: "Description"},
	"opv2:err:":   {pad: "Message"},
	"opv2:log:":   {pad: "Message"},
	"opv2:op:":    {pad: "ProgressMessage"},
	"opv2:row:":   {pad: "reason"},
	"opv3:run:":   {pad: "error"},
	"apikey:":     {hash: "token_hash"},
}

// holeKind is what a clone writes into one hole of its template.
type holeKind uint8

const (
	holeSuffix holeKind = iota // the clone's key suffix, quoted (the hole spans a quoted occurrence of the template's own)
	holeHash                   // hex SHA-256 of the clone's key suffix (the hole spans the template's 64 hex digits)
)

// hashHexLen is the length of a hex SHA-256.
const hashHexLen = 2 * sha256.Size

// template is a stage-1 (or overlay) row prepared for cloning: the value is cut
// at every hole, a quoted occurrence of its own key's suffix or its shape's
// hash field, so each clone writes its own there, and, when the family names a
// pad field the row holds, one insertion point where the value grows or
// shrinks to the size the family needs.
//
// Only that field is resized, so the clone still decodes as the template did
// and every typed field is the template's. A value with no such field, and
// any value that is not JSON, is cloned at its own size: the plan gives the
// family's remaining bytes to its resizable clones.
type template struct {
	segs    [][]byte   // value pieces between the holes
	holes   []holeKind // holes[i] lies between segs[i] and segs[i+1]
	baseLen int        // sum of the pieces' lengths
	// sized is true when the clone can be resized: padSeg/padOff is the
	// insertion point (inside segs[padSeg], at padOff, just before the pad
	// field's closing quote); removable bytes right before it may be dropped
	// to shrink the value.
	sized     bool
	padSeg    int
	padOff    int
	removable int
}

// compileTemplate prepares one row of family prefix, with the family's shape.
func compileTemplate(prefix string, key, value []byte, shape rowShape) template {
	t := template{segs: [][]byte{value}, baseLen: len(value)}
	if !json.Valid(value) {
		return t
	}
	type cut struct {
		at, end int
		kind    holeKind
	}
	var cuts []cut
	suffix := strings.TrimPrefix(string(key), prefix)
	if len(suffix) >= minSubstituteLen {
		quoted := []byte(strconv.Quote(suffix))
		// A suffix that needs JSON escaping is not rewritten: strconv's Go
		// quoting and JSON's agree only on plain ASCII.
		if string(quoted) == `"`+suffix+`"` {
			for from := 0; ; {
				i := bytes.Index(value[from:], quoted)
				if i < 0 {
					break
				}
				cuts = append(cuts, cut{from + i, from + i + len(quoted), holeSuffix})
				from += i + len(quoted)
			}
		}
	}
	if shape.hash != "" {
		if start, end, ok := stringField(value, shape.hash); ok && end-start == hashHexLen && isHex(value[start:end]) {
			cuts = append(cuts, cut{start, end, holeHash})
		}
	}
	slices.SortFunc(cuts, func(a, b cut) int { return a.at - b.at })
	for i := 1; i < len(cuts); i++ {
		if cuts[i].at < cuts[i-1].end {
			return t // overlapping holes: no such row is written by any registered family
		}
	}
	padAt, sized := -1, false
	if shape.pad != "" {
		if _, end, ok := stringField(value, shape.pad); ok {
			padAt, sized = end, true
			for _, c := range cuts {
				if padAt >= c.at && padAt < c.end {
					sized = false // the field is the key suffix itself: it names the row
				}
			}
		}
	}
	t.segs, t.holes, t.baseLen = nil, nil, 0
	prev := 0
	for _, c := range cuts {
		t.segs = append(t.segs, value[prev:c.at])
		t.holes = append(t.holes, c.kind)
		prev = c.end
	}
	t.segs = append(t.segs, value[prev:])
	for _, s := range t.segs {
		t.baseLen += len(s)
	}
	if !sized {
		return t
	}
	// Map the padding point (an offset into value) to a segment; it lies in
	// no hole (checked above).
	off := 0
	for i, s := range t.segs {
		if padAt >= off && padAt <= off+len(s) {
			t.padSeg, t.padOff = i, padAt-off
			break
		}
		if i < len(cuts) {
			off = cuts[i].end
		}
	}
	t.sized, t.removable = true, removableTail(value[:padAt])
	return t
}

// holeLen is the length a clone writes into a hole of kind k for a key suffix
// of suffixLen bytes.
func holeLen(k holeKind, suffixLen int) int {
	if k == holeHash {
		return hashHexLen
	}
	return suffixLen + 2
}

// size is the length of the clone for a key suffix of suffixLen bytes with no
// resizing.
func (t *template) size(suffixLen int) int {
	n := t.baseLen
	for _, k := range t.holes {
		n += holeLen(k, suffixLen)
	}
	return n
}

// build appends a clone of the template for the key suffix ns, of length want
// when the template can be resized to it, to dst[:0].
func (t *template) build(dst []byte, ns string, want int) []byte {
	dst = dst[:0]
	pad, cut := 0, 0
	if t.sized {
		if d := want - t.size(len(ns)); d > 0 {
			pad = d
		} else if d < 0 {
			cut = min(-d, t.removable)
		}
	}
	for i, s := range t.segs {
		if i > 0 {
			switch t.holes[i-1] {
			case holeHash:
				dst = appendSuffixHash(dst, ns)
			default:
				dst = append(dst, '"')
				dst = append(dst, ns...)
				dst = append(dst, '"')
			}
		}
		if t.sized && i == t.padSeg {
			dst = append(dst, s[:t.padOff-cut]...)
			dst = appendPad(dst, pad, ns)
			dst = append(dst, s[t.padOff:]...)
			continue
		}
		dst = append(dst, s...)
	}
	return dst
}

// appendSuffixHash appends the hex SHA-256 of a generated row's key suffix:
// distinct per row of a family, as the suffix is, and never a stage-1 row's.
func appendSuffixHash(dst []byte, ns string) []byte {
	sum := sha256.Sum256([]byte("r2corpus-shaped " + ns))
	return hex.AppendEncode(dst, sum[:])
}

// isHex reports a run of hex digits.
func isHex(b []byte) bool {
	for _, c := range b {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return len(b) > 0
}

// stringField finds the top-level field name of a JSON object whose value is
// a string, and returns the offsets of the string's content: start just after
// its opening quote, end at its closing quote. A later duplicate of the name
// wins, as it does for encoding/json.
func stringField(doc []byte, name string) (start, end int, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	depth := 0
	expectKey := false
	key := ""
	for {
		before := int(dec.InputOffset())
		tok, err := dec.Token()
		if err == io.EOF {
			return start, end, ok
		}
		if err != nil {
			return 0, 0, false
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				depth++
				expectKey = depth == 1
			case '[':
				depth++
			default:
				depth--
				expectKey = depth == 1
			}
			continue
		case string:
			if depth == 1 && expectKey {
				key, expectKey = v, false
				continue
			}
			if depth == 1 && key == name {
				closing := int(dec.InputOffset()) - 1
				open := bytes.IndexByte(doc[before:], '"')
				if open >= 0 && closing >= before+open && doc[closing] == '"' {
					start, end, ok = before+open+1, closing, true
				}
			}
		}
		if depth == 1 {
			expectKey = true
		}
	}
}

// padWords is the vocabulary of the padding text. Production values are text
// that a block compressor shrinks by a few times; a run of one byte would
// compress away and a random stream not at all, so the padding is words in an
// order fixed by the row's key. Plain ASCII with no quote or backslash, so it
// is valid inside any JSON string.
var padWords = [...]string{
	"batarang", "gotham", "arkham", "wayne", "manor", "cowl", "cape", "joker", "riddler", "penguin",
	"catwoman", "robin", "nightwing", "batgirl", "alfred", "gordon", "bane", "scarecrow", "crane", "dent",
	"harley", "quinn", "ivy", "freeze", "croc", "ra", "ghul", "talia", "lucius", "fox",
	"batmobile", "batcave", "belt", "grapnel", "signal", "tower", "asylum", "narrows", "robinson", "park",
}

// appendPad appends exactly n bytes of padding text to dst, chosen by a
// xorshift stream seeded from seed, so the same row always gets the same text.
func appendPad(dst []byte, n int, seed string) []byte {
	x := uint64(0x9e3779b97f4a7c15)
	for i := 0; i < len(seed); i++ {
		x = (x ^ uint64(seed[i])) * 0x100000001b3
	}
	for n > 0 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		w := padWords[x%uint64(len(padWords))]
		if n <= len(w) {
			return append(dst, w[:n]...)
		}
		dst = append(dst, w...)
		dst = append(dst, ' ')
		n -= len(w) + 1
	}
	return dst
}

// removableTail is how many bytes at the end of a string's content may be
// dropped and leave valid JSON: plain printable ASCII with no backslash in it,
// and, when a backslash precedes the run, not its first five bytes (they may
// be the tail of an escape such as é).
func removableTail(before []byte) int {
	n := 0
	for i := len(before) - 1; i >= 0; i-- {
		c := before[i]
		if c == '\\' {
			return max(0, n-5)
		}
		if c == '"' {
			return n // the string's opening quote: all of the content is plain
		}
		if c < 0x20 || c > 0x7e {
			return max(0, n-5)
		}
		n++
	}
	return n
}
