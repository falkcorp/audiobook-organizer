// file: internal/updater/appid_test.go
// version: 1.1.0
// guid: e7f9a1b3-4d6c-4e80-8f25-3b4c5d6e7f80
// last-edited: 2026-11-18

package updater_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/updater"
)

// TestAppIDIsTheBooksAppID: the updater's self-test holds a downloaded binary
// to the app field its --version-json prints, which main.go takes from
// books.AppID. internal/updater cannot import internal/books (books imports
// the server, which imports this package), so the two are held equal here.
func TestAppIDIsTheBooksAppID(t *testing.T) {
	if updater.AppID != books.AppID {
		t.Fatalf("updater.AppID = %q, books.AppID = %q: a release's --version-json would fail the self-test", updater.AppID, books.AppID)
	}
}
