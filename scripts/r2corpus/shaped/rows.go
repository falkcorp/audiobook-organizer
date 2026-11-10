// file: scripts/r2corpus/shaped/rows.go
// version: 1.0.0
// guid: f846ac1d-7a69-4229-9307-6bbe75cbc88c
// last-edited: 2026-11-10

//go:build r2corpus_shaped

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// minSubstituteLen is the shortest template key suffix whose quoted occurrences
// in the value are rewritten to a generated row's own suffix. A shorter suffix
// ("1") would rewrite unrelated values.
const minSubstituteLen = 6

// template is a stage-1 (or overlay) row prepared for cloning: the value is cut
// at every occurrence of its own key's suffix, so each clone can name its own
// key where the template named its own, and one insertion point is chosen where
// the value grows or shrinks to the size the family needs.
//
// Only a JSON value is resized, and only inside its longest string value (never
// a key or a number), so the clone still decodes as the template did. Any other
// value is cloned verbatim and its size is reported, not forced.
type template struct {
	segs    [][]byte // value pieces between the suffix occurrences
	baseLen int      // sum of the pieces' lengths
	// sized is true when the clone can be resized: a JSON value with a string
	// value to pad. padSeg/padOff is the insertion point (inside segs[padSeg],
	// at padOff, just before a string's closing quote); removable bytes right
	// before it may be dropped to shrink the value.
	sized     bool
	padSeg    int
	padOff    int
	removable int
}

// compileTemplate prepares one row of family prefix.
func compileTemplate(prefix string, key, value []byte) template {
	t := template{segs: [][]byte{value}, baseLen: len(value)}
	if !json.Valid(value) {
		return t
	}
	suffix := strings.TrimPrefix(string(key), prefix)
	pad, ok := padPoint(value, suffix)
	if !ok {
		return t
	}
	var cuts []int // start offset of each quoted suffix occurrence
	if len(suffix) >= minSubstituteLen {
		quoted := []byte(strconv.Quote(suffix))
		// A suffix that needs JSON escaping is not rewritten: strconv's Go
		// quoting is not JSON's.
		if string(quoted) == `"`+suffix+`"` {
			for from := 0; ; {
				i := bytes.Index(value[from:], quoted)
				if i < 0 {
					break
				}
				cuts = append(cuts, from+i)
				from += i + len(quoted)
			}
		}
		qlen := len(quoted)
		t.segs, t.baseLen = nil, 0
		prev := 0
		for _, c := range cuts {
			t.segs = append(t.segs, value[prev:c])
			prev = c + qlen
		}
		t.segs = append(t.segs, value[prev:])
		for _, s := range t.segs {
			t.baseLen += len(s)
		}
		// Map the padding point (an offset into value) to a segment. It never
		// lies inside an occurrence (padPoint skips the suffix's own string).
		off := 0
		for i, s := range t.segs {
			if pad.at >= off && pad.at <= off+len(s) {
				t.padSeg, t.padOff = i, pad.at-off
				break
			}
			off += len(s) + qlen
		}
	} else {
		t.padSeg, t.padOff = 0, pad.at
	}
	t.sized, t.removable = true, pad.removable
	return t
}

// size is the length of the clone for a key suffix of suffixLen bytes with no
// resizing.
func (t *template) size(suffixLen int) int {
	if len(t.segs) == 1 {
		return t.baseLen
	}
	return t.baseLen + (len(t.segs)-1)*(suffixLen+2)
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
			dst = append(dst, '"')
			dst = append(dst, ns...)
			dst = append(dst, '"')
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

// padding is where a JSON value grows or shrinks: at is the offset of a string
// value's closing quote, removable the plain bytes just before it that can be
// dropped without breaking an escape.
type padding struct {
	at, removable int
}

// padPoint finds the longest string value of a JSON document (never an object
// key, and never the string equal to skip, the row's own id) and returns where
// to resize it.
func padPoint(doc []byte, skip string) (padding, bool) {
	type frame struct{ obj, expectKey bool }
	var (
		stack []frame
		best  padding
		bestN = -1
	)
	dec := json.NewDecoder(bytes.NewReader(doc))
	// valueDone marks the value just read as consumed by its parent object.
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].obj {
			stack[n-1].expectKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return padding{}, false
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{obj: true, expectKey: true})
			case '[':
				stack = append(stack, frame{})
			default:
				stack = stack[:len(stack)-1]
				valueDone()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].obj && stack[n-1].expectKey {
				stack[n-1].expectKey = false // an object key
				continue
			}
			if end := int(dec.InputOffset()); v != skip && len(v) > bestN && end >= 1 && doc[end-1] == '"' {
				best = padding{at: end - 1, removable: removableTail(doc[:end-1])}
				bestN = len(v)
			}
			valueDone()
		default:
			valueDone()
		}
	}
	return best, bestN >= 0
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
