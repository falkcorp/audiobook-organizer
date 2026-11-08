// file: internal/books/activity_parity_test.go
// version: 1.0.1
// guid: f2fd308d-5301-467c-9794-69b349635f32
// last-edited: 2026-11-08

package books

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

func nonEmptyLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestActivityFeed_SameEntriesAsHEAD_NoCoreRows (R1-15 review M2): the lines
// ABO logs through slog's default make the same Activity entries under the
// composed App as under HEAD, where NewServer made a text handler over the
// activity writer slog's default. Summary, level, source, operation and every
// attribute (what Details is built from) are equal, and no entry gains a
// module attribute. Core's own lines (here the OTel error handler, which logs
// through core's otel scope) are printed but make no Activity entry.
func TestActivityFeed_SameEntriesAsHEAD_NoCoreRows(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	feed := &syncBuffer{}
	c.mod.logOut.setActivity(feed)

	emit := func() {
		slog.Info("starting run", "op_id", "01BATCAVE", "def", "library.scan", "component", "operations")
		logger.New("scanner").Info("scan finished for %s", "Arkham Asylum")
		slog.Warn("cover art saved to", "book_id", "01GOTHAM", "count", 3)
	}
	emit()
	otel.Handle(errors.New("the gotham exporter refused the batch"))

	var headBuf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&headBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	emit()
	slog.SetDefault(prev)

	r1, head := nonEmptyLines(feed.String()), nonEmptyLines(headBuf.String())
	require.Len(t, head, 3, "HEAD's lines:\n%s", headBuf.String())
	require.Len(t, r1, 3, "only the three process-default lines reach the feed:\n%s", feed.String())
	for i := range head {
		ph, pr := activity.ParseLogLineFull(head[i]), activity.ParseLogLineFull(r1[i])
		assert.Equal(t, activity.RenderSummary(ph), activity.RenderSummary(pr), "summary of line %d", i)
		assert.Equal(t, ph.Level, pr.Level, "level of line %d", i)
		assert.Equal(t, ph.Source, pr.Source, "source of line %d", i)
		assert.Equal(t, ph.OpID, pr.OpID, "operation of line %d", i)
		assert.Equal(t, ph.Component, pr.Component, "component of line %d", i)
		assert.Equal(t, ph.Attrs, pr.Attrs, "attributes (Details) of line %d", i)
		assert.NotContains(t, r1[i], "module=", "line %d", i)
	}

	assert.NotContains(t, feed.String(), "gotham exporter", "core's own line became an Activity entry")
	var coreLine string
	for _, l := range nonEmptyLines(c.newLog.String()) {
		if strings.Contains(l, "gotham exporter") {
			coreLine = l
		}
	}
	require.NotEmpty(t, coreLine, "core's own line must still be printed:\n%s", c.newLog.String())
	assert.Contains(t, coreLine, " module=core ", "core's own line keeps its module")
}

func TestScopedLine(t *testing.T) {
	cases := []struct {
		name, line string
		want       bool
	}{
		{"process, quoted msg", `time=t level=INFO msg="starting run" op_id=01BATCAVE` + "\n", false},
		{"process, bare msg", "time=t level=INFO msg=ready count=3\n", false},
		{"process, no attrs", "time=t level=INFO msg=ready\n", false},
		{"process, module attr later", `time=t level=INFO msg="joker escaped" wing=east module=core` + "\n", false},
		{"process, module in message", `time=t level=INFO msg="saw \" module=core\" on the wall" k=v` + "\n", false},
		{"core", `time=t level=WARN msg="kernel line" module=core subsystem=kernel` + "\n", true},
		{"core, bare msg", "time=t level=INFO msg=ready module=core subsystem=metrics\n", true},
		{"module logger", `time=t level=INFO msg="books line" module=books` + "\n", true},
		{"with source", `time=t level=INFO source=/x/y.go:1 msg="kernel line" module=core` + "\n", true},
		{"json", `{"time":"t","level":"INFO","msg":"kernel line","module":"core"}` + "\n", false},
		{"unterminated quote", `time=t level=INFO msg="never closed module=core`, false},
	}
	for _, tc := range cases {
		if got := scopedLine([]byte(tc.line)); got != tc.want {
			t.Errorf("%s: scopedLine(%q) = %v, want %v", tc.name, tc.line, got, tc.want)
		}
	}
}
