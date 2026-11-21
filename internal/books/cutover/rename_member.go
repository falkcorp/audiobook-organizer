// file: internal/books/cutover/rename_member.go
// version: 1.0.0
// guid: 5d0c8f3a-2b6e-4a71-9c14-e7a3b8d2f605
// last-edited: 2026-11-20

package cutover

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A TargetMember row lives inside a registered map-valued key (core's
// core.tasks.windows: one key whose JSON value maps a window name to its
// {start, end, enabled}). These two functions are the only code that addresses
// a member, so M2's staging, the Engine's projection and the Engine's write
// agree on what "nightly.start" means.

var errMemberPath = errors.New("cutover: a member path is dotted names")

// MemberOf is the member rel ("nightly.start") of the JSON object whole. It
// reports false when whole is not an object holding every name on the way, which
// is how an entry core's default does not declare reads.
func MemberOf(whole json.RawMessage, rel string) (json.RawMessage, bool, error) {
	names, err := memberNames(rel)
	if err != nil {
		return nil, false, err
	}
	cur := whole
	for _, n := range names {
		var obj map[string]json.RawMessage
		if json.Unmarshal(cur, &obj) != nil {
			return nil, false, nil
		}
		next, ok := obj[n]
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return cur, true, nil
}

// WithMember is whole with the member rel set to v, creating the objects on the
// way and keeping every other member as it was. A whole that is not an object
// (empty, null) is a fresh one. The result is canonical JSON (keys sorted).
func WithMember(whole json.RawMessage, rel string, v json.RawMessage) (json.RawMessage, error) {
	names, err := memberNames(rel)
	if err != nil {
		return nil, err
	}
	return setMember(whole, names, v)
}

func setMember(whole json.RawMessage, names []string, v json.RawMessage) (json.RawMessage, error) {
	if len(names) == 0 {
		return v, nil
	}
	obj := map[string]json.RawMessage{}
	if len(whole) > 0 && string(whole) != "null" {
		if err := json.Unmarshal(whole, &obj); err != nil {
			return nil, fmt.Errorf("cutover: the map value is not an object: %w", err)
		}
	}
	child, err := setMember(obj[names[0]], names[1:], v)
	if err != nil {
		return nil, err
	}
	obj[names[0]] = child
	return json.Marshal(obj)
}

func memberNames(rel string) ([]string, error) {
	if rel == "" {
		return nil, errMemberPath
	}
	names := strings.Split(rel, ".")
	if slices.Contains(names, "") {
		return nil, errMemberPath
	}
	return names, nil
}
