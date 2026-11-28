// file: internal/books/floor_release_test.go
// version: 0.1.0
// guid: c4a9f3e1-2b75-4d08-9a63-71e5d0b8c2f4
// last-edited: 2026-11-22

//go:build release

package books

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUpgradeFloorTagIsNotPlaceholder is the release gate for books' floor
// (R3-02d). It exists only under -tags release, so a development build never
// sees it, and fails while InstallFirst is still the placeholder: a refusal
// that told an operator to "install v0.0.0-r2-placeholder first" would be a
// dead end. Run it in the R3 release job:
//
//	go test -tags release -run TestUpgradeFloorTagIsNotPlaceholder ./internal/books/
//
// The tag to put in floor.go comes only from OWNER-05's tracking-issue record.
func TestUpgradeFloorTagIsNotPlaceholder(t *testing.T) {
	tag := upgradeFloor().InstallFirst
	assert.NotEqual(t, floorPlaceholderTag, tag,
		"internal/books/floor.go still declares the R2 placeholder tag: replace the InstallFirst literal with the tag OWNER-05 recorded")
	assert.NotContains(t, tag, "placeholder")
	assert.Regexp(t, floorTagPattern, tag, "InstallFirst must be a plain release tag (vX.Y.Z), as OWNER-05's record names it")
}
