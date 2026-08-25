// file: internal/server/handlers/abs/browse_unsupported_sort_test.go
// version: 1.0.0
// guid: 2a9f4d13-8b07-4e56-91c2-5d3e08a7f6b4
// last-edited: 2026-08-25

package abs

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

// A sort this server cannot perform must not be answered in silence.
//
// absSortField returns "" for anything absSortFields does not know, and ""
// means "no ordering requested" everywhere downstream — so the client gets a
// 200 and the store's default order. warnUnindexedSort cannot report it: its
// first line returns early on field == "", so the one case that most needed
// reporting was the one case it skipped. The client menu offers 14 sorts and
// the map covers 8.
//
// bus* names are task-unique per repo convention for package-shared helpers.

func busCapture(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return &buf, func() { slog.SetDefault(prev) }
}

func busCtx(q string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?"+q, nil)
	return c
}

// busReset clears the once-per-process rate limiters so each subtest starts
// from a state where a warning is still possible. Without it the first subtest
// to run consumes the only warning and the rest pass for the wrong reason.
func busReset() {
	absUnsupportedSortWarned = sync.Map{}
	absUnindexedSortWarned = sync.Map{}
}

func TestUnsupportedSortIsReported(t *testing.T) {
	t.Run("unsupported sort warns and names the alternatives", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		f := absItemFilter(busCtx("sort=media.metadata.fileModified"))
		if f.SortBy != "" {
			t.Fatalf("fixture assumes this sort is unmapped; absSortField returned %q — "+
				"if it was just mapped, move it to the supported case below", f.SortBy)
		}
		got := buf.String()
		if !strings.Contains(got, "no field for") {
			t.Errorf("an unsupported sort was answered in silence; log was:\n%s", got)
		}
		// The warning must name what IS supported, or it tells the reader only
		// that something failed.
		if !strings.Contains(got, "publishedyear") {
			t.Errorf("warning does not list the supported sorts; log was:\n%s", got)
		}
	})

	t.Run("supported sort does not warn", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		f := absItemFilter(busCtx("sort=media.metadata.publishedYear"))
		if f.SortBy != "year" {
			t.Fatalf("absSortField = %q, want year", f.SortBy)
		}
		if strings.Contains(buf.String(), "no field for") {
			t.Errorf("a supported sort produced the unsupported-sort warning:\n%s", buf.String())
		}
	})

	t.Run("no sort at all does not warn", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		absItemFilter(busCtx(""))
		if strings.Contains(buf.String(), "no field for") {
			t.Errorf("absent sort must be silent, it is not an error:\n%s", buf.String())
		}
	})

	t.Run("warns once per distinct sort string", func(t *testing.T) {
		busReset()
		buf, restore := busCapture(t)
		defer restore()

		for i := 0; i < 5; i++ {
			absItemFilter(busCtx("sort=media.metadata.fileBirthtime"))
		}
		if n := strings.Count(buf.String(), "no field for"); n != 1 {
			t.Errorf("rate limit failed: %d warnings for the same sort, want 1", n)
		}
		// A DIFFERENT unsupported sort must still be reported — otherwise one
		// noisy client silences every other gap.
		absItemFilter(busCtx("sort=progress"))
		if n := strings.Count(buf.String(), "no field for"); n != 2 {
			t.Errorf("a second, distinct unsupported sort was suppressed: got %d warnings, want 2", n)
		}
	})
}
