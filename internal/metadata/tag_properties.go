// file: internal/metadata/tag_properties.go
// version: 1.3.0
// guid: 6f3c9a27-4e1d-4b85-9c20-7a5e1d8b3f64
// last-edited: 2026-09-14

package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// tagProperties maps each tag key the organizer writes and records for undo to
// exactly the TagLib properties the organize writes for it: WriteTagProperties
// writes a plain value to all of them, ReadTagProperties snapshots all of them
// and the revert puts back only them. The organize writes the author to ARTIST
// and ALBUMARTIST (Album Artist is the author, owner decision 2026-09-14) and
// the narrator to NARRATOR and PERFORMER. COMPOSER is never written, so it is
// not in the artist key: a snapshot that recorded it made the undo write an
// old COMPOSER back over a later edit.
//
// A revert reads and writes through this table and never through
// buildWriteTagMap, which is not the inverse of the reader: it sets ALBUM=""
// for a lone series, drops every empty value (so a tag could never be
// removed), and ignores album_artist and composer.
//
// A key with more than one property is recorded as a per-property snapshot
// (encodeTagSnapshot): each property's own pre-write value, or its absence.
// Those properties legitimately differ (ARTIST a co-author while ALBUMARTIST
// is the author; PERFORMER and NARRATOR naming different readers), so one
// string could not restore them, and the revert compares and restores each
// property on its own (PlanSnapshotRestore).
var tagProperties = map[string][]string{
	"title":        {"TITLE"},
	"album":        {"ALBUM"},
	"artist":       {"ARTIST", "ALBUMARTIST"},
	"album_artist": {"ALBUMARTIST"},
	"composer":     {"COMPOSER"},
	"genre":        {"GENRE"},
	"year":         {"DATE"},
	"narrator":     {"NARRATOR", "PERFORMER"},
	"series":       {"SERIES"},
	"language":     {"LANGUAGE"},
	"publisher":    {"PUBLISHER"},

	// One-property aliases for undo rows recorded before per-property
	// snapshots (see LegacyTagKey). They are never written by the organize.
	"artist_tag_only":   {"ARTIST"},
	"narrator_tag_only": {"NARRATOR"},
}

// legacyTagKeys maps a multi-property key to the one-property key its undo rows
// addressed before per-property snapshots. Those rows hold one plain value and
// were written when the organize wrote artist to ARTIST only and narrator to
// NARRATOR only, so they revert exactly by writing that one property and leave
// ALBUMARTIST, COMPOSER and PERFORMER alone.
var legacyTagKeys = map[string]string{
	"artist":   "artist_tag_only",
	"narrator": "narrator_tag_only",
}

// tagSnapshotPrefix marks an undo value that holds one pre-write value per
// property (JSON object, property -> value, null for absent).
const tagSnapshotPrefix = "\x00tag-props:v1:"

// ErrTagKeyNotWritable is returned for a tag key with no file property.
var ErrTagKeyNotWritable = errors.New("tag key has no file property to write")

// TagProperty returns every TagLib property a write of key can change.
func TagProperty(key string) ([]string, bool) {
	p, ok := tagProperties[key]
	return p, ok
}

// IsTagSnapshot reports whether v is a per-property snapshot undo value.
func IsTagSnapshot(v string) bool {
	return strings.HasPrefix(v, tagSnapshotPrefix)
}

// LegacyTagKey returns the one-property key that a plain (non-snapshot) undo
// value recorded for key must be read and reverted through, and false for a key
// whose plain values are already exact (every single-property key).
func LegacyTagKey(key string) (string, bool) {
	k, ok := legacyTagKeys[key]
	return k, ok
}

func encodeTagSnapshot(snap map[string]*string) string {
	b, err := json.Marshal(snap)
	if err != nil { // a map of strings always marshals
		panic(fmt.Sprintf("encode tag snapshot: %v", err))
	}
	return tagSnapshotPrefix + string(b)
}

func decodeTagSnapshot(v string) (map[string]*string, error) {
	var snap map[string]*string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(v, tagSnapshotPrefix)), &snap); err != nil {
		return nil, fmt.Errorf("undo value is not a readable tag snapshot: %w", err)
	}
	return snap, nil
}

// ReadTagProperties returns the undo value of every key in the table, the
// value the organize records before it writes. A single-property key reads as
// its value ("" when the file does not carry it). A multi-property key reads as
// a snapshot of each property. A key is left out (unknown) when one of its
// properties holds more than one value, because one string per property cannot
// put it back as it was.
func ReadTagProperties(path string) (map[string]string, error) {
	raw, err := readTagsWithTaglib(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(tagProperties))
next:
	for key, props := range tagProperties {
		if len(props) == 1 {
			switch vals := raw[props[0]]; len(vals) {
			case 0:
				out[key] = ""
			case 1:
				out[key] = vals[0]
			}
			continue
		}
		snap := make(map[string]*string, len(props))
		for _, prop := range props {
			switch vals := raw[prop]; len(vals) {
			case 0:
				snap[prop] = nil
			case 1:
				v := vals[0]
				snap[prop] = &v
			default:
				continue next
			}
		}
		out[key] = encodeTagSnapshot(snap)
	}
	return out, nil
}

// ReadTagValues returns every key's current plain value: the one value every
// property it writes holds, or "" when none is present. A key is left out when
// those properties are not all in the same state (one absent, two different
// values, or one holding several), so the unchanged-tag filter writes it
// (ARTIST already the author with ALBUMARTIST absent is not unchanged).
func ReadTagValues(path string) (map[string]string, error) {
	raw, err := readTagsWithTaglib(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(tagProperties))
	for key := range tagProperties {
		if v, ok := samePropertyValue(raw, tagProperties[key]); ok {
			out[key] = v
		}
	}
	return out, nil
}

// samePropertyValue returns the value every prop holds ("" when all are
// absent), and false unless all props are absent or all hold the same single
// value.
func samePropertyValue(raw map[string][]string, props []string) (string, bool) {
	var value string
	for i, prop := range props {
		vals := raw[prop]
		if len(vals) > 1 {
			return "", false
		}
		v, present := "", len(vals) == 1
		if present {
			v = vals[0]
		}
		if i > 0 && (present != (value != "") || v != value) {
			return "", false
		}
		value = v
	}
	return value, true
}

// SnapshotRestore is the per-property plan for reverting one snapshot undo
// row (PlanSnapshotRestore).
type SnapshotRestore struct {
	// Write is the snapshot to pass to WriteTagProperties for the key: only
	// the properties being put back. "" when nothing is to be written.
	Write string
	// Restored names the properties Write puts back.
	Restored []string
	// Already names the properties that already hold their pre-write state.
	Already []string
	// Kept names the properties changed since the organize wrote them; they
	// are left as they are.
	Kept []string
}

// PlanSnapshotRestore compares, property by property, the file's current
// snapshot of key (ReadTagProperties) with the snapshot the organize recorded
// (oldSnap) and the plain value it wrote (newValue). A property that still
// holds newValue is put back to its recorded value, or removed when it was
// absent; one that already holds its recorded state needs nothing; any other
// is kept as a later change. A property the recorded snapshot names but the
// key no longer owns (a COMPOSER in a snapshot taken before COMPOSER left the
// artist key) was never written by the organize and is left alone.
func PlanSnapshotRestore(key, oldSnap, newValue, currentSnap string) (SnapshotRestore, error) {
	var plan SnapshotRestore
	props, ok := tagProperties[key]
	if !ok {
		return plan, fmt.Errorf("%w: %q", ErrTagKeyNotWritable, key)
	}
	old, err := decodeTagSnapshot(oldSnap)
	if err != nil {
		return plan, fmt.Errorf("tag %s: %w", key, err)
	}
	if !IsTagSnapshot(currentSnap) {
		return plan, fmt.Errorf("tag %s: the current value is not a tag snapshot", key)
	}
	cur, err := decodeTagSnapshot(currentSnap)
	if err != nil {
		return plan, fmt.Errorf("tag %s: %w", key, err)
	}
	write := map[string]*string{}
	for _, prop := range props {
		want, recorded := old[prop]
		if !recorded {
			continue
		}
		have := cur[prop]
		switch {
		case (want == nil && have == nil) || (want != nil && have != nil && *want == *have):
			plan.Already = append(plan.Already, prop)
		case have != nil && *have == newValue:
			write[prop] = want
			plan.Restored = append(plan.Restored, prop)
		default:
			plan.Kept = append(plan.Kept, prop)
		}
	}
	if len(write) > 0 {
		plan.Write = encodeTagSnapshot(write)
	}
	return plan, nil
}

// WriteTagProperties writes exactly the given keys and leaves every other
// property as it is. A plain value is written to each property the key writes
// ("" removes them); a snapshot (from ReadTagProperties) puts each property it
// names back to its own value and removes the ones it records as absent. A key
// with no property, or a snapshot naming a property outside its key, fails
// before anything is written. It then reads the file back and fails unless
// every property holds what was asked for, so a writer that drops a value, or
// a removal it did not make, is an error and not a success.
func WriteTagProperties(path string, values map[string]string) error {
	tags := make(map[string][]string, len(values))
	for key, v := range values {
		props, ok := tagProperties[key]
		if !ok {
			return fmt.Errorf("%w: %q", ErrTagKeyNotWritable, key)
		}
		if IsTagSnapshot(v) {
			snap, err := decodeTagSnapshot(v)
			if err != nil {
				return fmt.Errorf("tag %s: %w", key, err)
			}
			for prop, val := range snap {
				if !slices.Contains(props, prop) {
					return fmt.Errorf("tag %s: snapshot names property %s, which the key does not own", key, prop)
				}
				if val == nil {
					tags[prop] = []string{}
				} else {
					tags[prop] = []string{*val}
				}
			}
			continue
		}
		for _, prop := range props {
			if v == "" {
				tags[prop] = []string{}
			} else {
				tags[prop] = []string{v}
			}
		}
	}
	if len(tags) == 0 {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("tag properties abs path: %w", err)
	}
	if err := writePropertiesWithTaglib(abs, tags); err != nil {
		return fmt.Errorf("write tag properties to %s: %w", path, err)
	}
	raw, err := readTagsWithTaglib(abs)
	if err != nil {
		return fmt.Errorf("read back tags of %s after the write: %w", path, err)
	}
	for prop, vals := range tags {
		if !slices.Equal(raw[prop], vals) && !(len(vals) == 0 && len(raw[prop]) == 0) {
			return fmt.Errorf("tag property %s of %s reads %q after the write, not %q", prop, path, raw[prop], vals)
		}
	}
	return nil
}
