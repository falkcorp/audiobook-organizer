// file: internal/metadata/tag_properties.go
// version: 1.1.0
// guid: 6f3c9a27-4e1d-4b85-9c20-7a5e1d8b3f64
// last-edited: 2026-09-14

package metadata

import (
	"errors"
	"fmt"
	"path/filepath"
)

// tagProperties maps each tag key the organizer records for undo to the
// TagLib properties that hold it. A revert puts one key back, so it reads and
// writes through this table and never through buildWriteTagMap, which is not
// the inverse of the reader: it fans artist out to ALBUMARTIST and writes
// COMPOSER="" (where this project keeps the narrator), sets ALBUM="" for a lone
// series, drops every empty value (so a tag could never be removed), and
// ignores album_artist and composer.
//
// narrator names two properties because buildWriteTagMap writes a narrator
// edit to both NARRATOR and PERFORMER. Undoing NARRATOR alone left PERFORMER
// holding the edited value, and a reader that consults PERFORMER still saw it.
// Every property a key names is read, written and removed together.
var tagProperties = map[string][]string{
	"title":        {"TITLE"},
	"album":        {"ALBUM"},
	"artist":       {"ARTIST"},
	"album_artist": {"ALBUMARTIST"},
	"composer":     {"COMPOSER"},
	"genre":        {"GENRE"},
	"year":         {"DATE"},
	"narrator":     {"NARRATOR", "PERFORMER"},
	"series":       {"SERIES"},
	"language":     {"LANGUAGE"},
	"publisher":    {"PUBLISHER"},
}

// ErrTagKeyNotWritable is returned for a tag key with no file property.
var ErrTagKeyNotWritable = errors.New("tag key has no file property to write")

// TagProperty returns the TagLib properties that hold key.
func TagProperty(key string) ([]string, bool) {
	p, ok := tagProperties[key]
	return p, ok
}

// ReadTagProperties returns the file's value for every key in the table: ""
// when the file carries none of the key's properties. A key is left out
// (unknown) when one of its properties holds more than one value, or when two
// of its properties hold different values: one string cannot put them back as
// they were, so the organize records no restorable value and the revert
// refuses rather than overwrite either. An absent property is no value, not a
// competing one: a file carrying only PERFORMER="X" reads "X", which is what
// every reader returns for it, and the revert writes "X" back.
func ReadTagProperties(path string) (map[string]string, error) {
	raw, err := readTagsWithTaglib(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(tagProperties))
	for key, props := range tagProperties {
		if v, ok := agreedPropertyValue(raw, props); ok {
			out[key] = v
		}
	}
	return out, nil
}

// agreedPropertyValue returns the one value the present props hold ("" when
// none is present). Absent props are skipped. It returns false when a prop
// holds more than one value or two present props hold different values.
func agreedPropertyValue(raw map[string][]string, props []string) (string, bool) {
	value := ""
	for _, prop := range props {
		switch vals := raw[prop]; len(vals) {
		case 0:
			continue
		case 1:
			if value != "" && vals[0] != value {
				return "", false
			}
			value = vals[0]
		default:
			return "", false
		}
	}
	return value, true
}

// WriteTagProperties writes exactly the given keys, each to every property it
// names, and leaves every other property as it is; "" removes those
// properties. A key with no property fails before anything is written. It then
// reads the file back and fails unless every key's properties hold the value
// asked for, so a writer that drops a value, or a removal it did not make, is
// an error and not a success.
func WriteTagProperties(path string, values map[string]string) error {
	tags := make(map[string][]string, len(values))
	for key, v := range values {
		props, ok := tagProperties[key]
		if !ok {
			return fmt.Errorf("%w: %q", ErrTagKeyNotWritable, key)
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
	got, err := ReadTagProperties(abs)
	if err != nil {
		return fmt.Errorf("read back tags of %s after the write: %w", path, err)
	}
	for key, want := range values {
		if cur, ok := got[key]; !ok || cur != want {
			return fmt.Errorf("tag %s of %s reads %q after the write, not %q", key, path, cur, want)
		}
	}
	return nil
}
