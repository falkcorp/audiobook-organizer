// file: internal/ai/result_count_error_test.go
// version: 1.0.0
// guid: 8e3f1a6c-2b7d-4c95-9a04-6d1e5f2c8b37
// last-edited: 2026-09-13

package ai

import (
	"errors"
	"strings"
	"testing"
)

// A short reply must surface as a *ResultCountError UNDER a *ReplyParseError:
// the scanner splits on the inner type, and its permanent-failure classifier
// relies on the outer one to avoid reading model-written text.
func TestParseBatchMetadataFromJSON_ShortReplyIsTypedCountError(t *testing.T) {
	_, err := parseBatchMetadataFromJSON(`{"results": [{"title": "Star Wars Heir to the Jedi"}]}`, 6)
	if err == nil {
		t.Fatal("a 1-result reply for 6 filenames was accepted")
	}
	if _, ok := err.(*ReplyParseError); !ok {
		t.Fatalf("outer error is %T, want *ReplyParseError", err)
	}
	ce, ok := errors.AsType[*ResultCountError](err)
	if !ok {
		t.Fatalf("no *ResultCountError in %v", err)
	}
	if ce.Got != 1 || ce.Expected != 6 {
		t.Errorf("ResultCountError = %+v, want Got 1 Expected 6", ce)
	}
	if !strings.Contains(err.Error(), "got 1 result(s) for 6 filename(s)") {
		t.Errorf("message changed: %v", err)
	}
}

// Other reply failures must NOT carry the type, or the scanner would split on
// a malformed reply.
func TestParseBatchMetadataFromJSON_MalformedReplyIsNotACountError(t *testing.T) {
	_, err := parseBatchMetadataFromJSON(`{"results": [`, 3)
	if err == nil {
		t.Fatal("malformed reply accepted")
	}
	if _, ok := errors.AsType[*ResultCountError](err); ok {
		t.Errorf("malformed reply classified as a count mismatch: %v", err)
	}
}
