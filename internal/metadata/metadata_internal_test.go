// file: internal/metadata/metadata_internal_test.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d

package metadata

import (
	"testing"

	"github.com/dhowden/tag"
)

func TestGetRawStringCaseInsensitive(t *testing.T) {
	raw := map[string]any{
		"Publisher": []string{"Podium Audio", "Other"},
	}

	got := getRawString(raw, "publisher")
	if got != "Podium Audio" {
		t.Fatalf("expected Podium Audio, got %q", got)
	}
}

func TestGetRawStringSkipsReleaseGroupTag(t *testing.T) {
	raw := map[string]any{
		"aART": []string{"[PZG]", "Greg Chun"},
	}

	got := getRawString(raw, "aART")
	if got != "Greg Chun" {
		t.Fatalf("expected Greg Chun, got %q", got)
	}
}

func TestGetRawStringFromTXXXComm(t *testing.T) {
	raw := map[string]any{
		"TXXX": &tag.Comm{Description: "NARRATOR", Text: "Jane Doe"},
	}

	got := getRawString(raw, "narrator")
	if got != "Jane Doe" {
		t.Fatalf("expected Jane Doe, got %q", got)
	}
}
