// file: internal/updater/appid_test.go
// version: 1.0.0
// guid: e7f9a1b3-4d6c-4e80-8f25-3b4c5d6e7f80
// last-edited: 2026-11-18

package updater_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/updater"
	coreupdater "github.com/falkcorp/mediacore/updater"
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

// TestCoreOptions: what app.Options.Updater carries for this app.
func TestCoreOptions(t *testing.T) {
	o := updater.CoreOptions()
	if o.AppID != books.AppID || o.BinaryName != "audiobook-organizer" || o.DefaultMode != coreupdater.ModeCheck {
		t.Errorf("CoreOptions = %+v", o)
	}
	if _, ok := o.Source.(coreupdater.TagSource); !ok {
		t.Error("the source cannot look a release up by tag, which `updater fetch-release` and a named apply need")
	}
	if _, ok := o.Source.(coreupdater.Binder); !ok {
		t.Error("the source takes no HTTP client or user agent from the updater")
	}
	if got := coreupdater.RawBinary(o.BinaryName)(coreupdater.Release{}, "linux", "arm64"); got != "audiobook-organizer-linux-arm64" {
		t.Errorf("the release asset is %q, want audiobook-organizer-linux-arm64", got)
	}
}
