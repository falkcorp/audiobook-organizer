// file: internal/syncapi/conformance/diff.go
// version: 1.0.0
// guid: 85152964-4de2-4224-a975-52d3d928382f
// last-edited: 2026-07-29

package conformance

import (
	"fmt"
	"reflect"
	"strconv"
)

// FindingKind classifies a single conformance defect.
type FindingKind string

const (
	// KindMissingField is the highest-severity finding: the fixture captured
	// from real ABS has a field our response omits. Clients hard-require
	// specific fields and fail opaquely when they are absent.
	KindMissingField FindingKind = "missing_field"
	// KindExtraField means we return a field ABS does not. Usually benign.
	KindExtraField FindingKind = "extra_field"
	// KindTypeMismatch means the field exists but has the wrong JSON type.
	KindTypeMismatch FindingKind = "type_mismatch"
	// KindLengthMismatch means two arrays differ in length.
	KindLengthMismatch FindingKind = "length_mismatch"
	// KindValueMismatch is only produced when Options.CompareValues is set.
	KindValueMismatch FindingKind = "value_mismatch"
)

// Finding is one conformance defect located by JSON path.
type Finding struct {
	Path string
	Kind FindingKind
	Want string
	Got  string
}

func (f Finding) String() string {
	if f.Path == "" {
		return fmt.Sprintf("%s: want %s, got %s", f.Kind, f.Want, f.Got)
	}
	return fmt.Sprintf("%s at %s: want %s, got %s", f.Kind, f.Path, f.Want, f.Got)
}

// Options tunes comparison strictness.
type Options struct {
	// CompareValues also compares scalar values. Off by default because
	// fixtures are normalized (volatile values are canonicalized), so
	// presence and type are the meaningful signal.
	CompareValues bool
	// IgnoreExtra suppresses KindExtraField findings.
	IgnoreExtra bool
}

// Compare walks want (the ABS fixture) against got (our response) and returns
// every conformance defect found. A nil/empty result means conformant.
func Compare(want, got any, opts Options) []Finding {
	var out []Finding
	compareValue("", want, got, opts, &out)
	return out
}

func compareValue(path string, want, got any, opts Options, out *[]Finding) {
	wt, gt := JSONType(want), JSONType(got)
	if wt != gt {
		*out = append(*out, Finding{Path: path, Kind: KindTypeMismatch, Want: wt, Got: gt})
		return
	}

	switch wt {
	case "object":
		compareObject(path, want.(map[string]any), got.(map[string]any), opts, out)
	case "array":
		compareArray(path, want.([]any), got.([]any), opts, out)
	default:
		if opts.CompareValues && !reflect.DeepEqual(want, got) {
			*out = append(*out, Finding{
				Path: path, Kind: KindValueMismatch,
				Want: fmt.Sprintf("%v", want), Got: fmt.Sprintf("%v", got),
			})
		}
	}
}

func compareObject(path string, want, got map[string]any, opts Options, out *[]Finding) {
	for k, wv := range want {
		child := joinPath(path, k)
		gv, ok := got[k]
		if !ok {
			*out = append(*out, Finding{
				Path: child, Kind: KindMissingField, Want: JSONType(wv), Got: "absent",
			})
			continue
		}
		compareValue(child, wv, gv, opts, out)
	}
	if opts.IgnoreExtra {
		return
	}
	for k, gv := range got {
		if _, ok := want[k]; !ok {
			*out = append(*out, Finding{
				Path: joinPath(path, k), Kind: KindExtraField, Want: "absent", Got: JSONType(gv),
			})
		}
	}
}

func compareArray(path string, want, got []any, opts Options, out *[]Finding) {
	if len(want) != len(got) {
		*out = append(*out, Finding{
			Path: path, Kind: KindLengthMismatch,
			Want: strconv.Itoa(len(want)), Got: strconv.Itoa(len(got)),
		})
	}
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		compareValue(fmt.Sprintf("%s[%d]", path, i), want[i], got[i], opts, out)
	}
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}
