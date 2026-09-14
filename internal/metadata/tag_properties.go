// file: internal/metadata/tag_properties.go
// version: 1.0.0
// guid: 6f3c9a27-4e1d-4b85-9c20-7a5e1d8b3f64
// last-edited: 2026-09-13

package metadata

import (
	"errors"
	"fmt"
	"path/filepath"
)

// tagProperties maps each tag key the organizer records for undo to the one
// TagLib property that holds it. A revert puts one key back, so it reads and
// writes through this table and never through buildWriteTagMap, which is not
// the inverse of the reader: it fans one key out to several properties (artist
// also sets ALBUMARTIST and writes COMPOSER="", where this project keeps the
// narrator), sets ALBUM="" for a lone series, drops every empty value (so a
// tag could never be removed), and ignores album_artist and composer.
var tagProperties = map[string]string{
	"title":        "TITLE",
	"album":        "ALBUM",
	"artist":       "ARTIST",
	"album_artist": "ALBUMARTIST",
	"composer":     "COMPOSER",
	"genre":        "GENRE",
	"year":         "DATE",
	"narrator":     "NARRATOR",
	"series":       "SERIES",
	"language":     "LANGUAGE",
	"publisher":    "PUBLISHER",
}

// ErrTagKeyNotWritable is returned for a tag key with no single file property.
var ErrTagKeyNotWritable = errors.New("tag key has no single file property to write")

// TagProperty returns the TagLib property that holds key.
func TagProperty(key string) (string, bool) {
	p, ok := tagProperties[key]
	return p, ok
}

// ReadTagProperties returns the file's value for every key in the table: ""
// for a property the file does not carry. A property holding more than one
// value is left out (unknown), because one string cannot put it back as it was.
func ReadTagProperties(path string) (map[string]string, error) {
	raw, err := readTagsWithTaglib(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(tagProperties))
	for key, prop := range tagProperties {
		switch vals := raw[prop]; len(vals) {
		case 0:
			out[key] = ""
		case 1:
			out[key] = vals[0]
		}
	}
	return out, nil
}

// WriteTagProperties writes exactly the given keys, each to its one property,
// and leaves every other property as it is; "" removes the property. A key with
// no property fails before anything is written. It then reads the file back and
// fails unless every property holds the value asked for, so a writer that
// drops a value, or a removal it did not make, is an error and not a success.
func WriteTagProperties(path string, values map[string]string) error {
	tags := make(map[string][]string, len(values))
	for key, v := range values {
		prop, ok := tagProperties[key]
		if !ok {
			return fmt.Errorf("%w: %q", ErrTagKeyNotWritable, key)
		}
		if v == "" {
			tags[prop] = []string{}
		} else {
			tags[prop] = []string{v}
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
