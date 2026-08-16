// file: internal/activity/writer_attrs_test.go
// version: 1.0.0
// guid: 2b7d4e91-6c05-4a83-91ff-0d5e7a3c8b46
// last-edited: 2026-08-16

package activity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The activity log showed rows that had lost the only information that made
// them worth logging: "cover art saved to" (to WHERE?) and "ISBN enrichment
// succeeded for" (for WHAT?). The sentence is the slog MESSAGE; the thing it is
// about is a slog ATTR, and the bridge kept only the message.
//
// A neighbouring row showed the OPPOSITE symptom — a raw slog line with quotes
// pasted into the summary. Both come from ONE defect: msg was located with
// strings.LastIndexByte(rest, '"'), which finds the last quote in the whole
// remaining line. With no quoted attr after msg it lands on the message's own
// closing quote by luck; with one, the message swallows the rest of the line.
func TestParseLogLineFull_KeepsAttrs(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantMessage string
		wantSummary string
		wantAttrs   map[string]string
	}{
		{
			name:        "sentence fragment ending in a preposition takes the value bare",
			line:        `time=2026-08-16T01:00:00.000-04:00 level=INFO msg="cover art saved to" path=/lib/Asimov/cover.jpg id=b1`,
			wantMessage: "cover art saved to",
			wantSummary: "cover art saved to /lib/Asimov/cover.jpg id=b1",
			wantAttrs:   map[string]string{"path": "/lib/Asimov/cover.jpg", "id": "b1"},
		},
		{
			name:        "for is a preposition too",
			line:        `time=2026-08-16T01:00:00.000-04:00 level=INFO msg="ISBN enrichment succeeded for" id=b42`,
			wantMessage: "ISBN enrichment succeeded for",
			wantSummary: "ISBN enrichment succeeded for b42",
			wantAttrs:   map[string]string{"id": "b42"},
		},
		{
			// The regression that produced the stray quote. Every attr after
			// msg is quoted, so LastIndexByte used to land at the very end of
			// the line.
			name:        "quoted attrs must not be swallowed into the message",
			line:        `time=2026-08-16T01:00:00.000-04:00 level=INFO msg="ISBN enrichment found" isbn="9780553293357" title="Foundation"`,
			wantMessage: "ISBN enrichment found",
			wantSummary: `ISBN enrichment found isbn=9780553293357 title=Foundation`,
			wantAttrs:   map[string]string{"isbn": "9780553293357", "title": "Foundation"},
		},
		{
			name:        "a complete sentence gets key=value appended, not a bare value",
			line:        `time=2026-08-16T01:00:00.000-04:00 level=WARN msg="tag writing failed" book_id=b7 error="disk full"`,
			wantMessage: "tag writing failed",
			wantSummary: `tag writing failed book_id=b7 error=disk full`,
			wantAttrs:   map[string]string{"book_id": "b7", "error": "disk full"},
		},
		{
			name:        "structural attrs are not repeated in the summary",
			line:        `time=2026-08-16T01:00:00.000-04:00 level=INFO msg="scan complete" op_id=op-9 component=scanner count=12`,
			wantMessage: "scan complete",
			wantSummary: "scan complete count=12",
			wantAttrs:   map[string]string{"count": "12"},
		},
		{
			name:        "a message with no attrs is unchanged",
			line:        `time=2026-08-16T01:00:00.000-04:00 level=INFO msg="activity log service initialized"`,
			wantMessage: "activity log service initialized",
			wantSummary: "activity log service initialized",
			wantAttrs:   map[string]string{},
		},
		{
			name:        "an escaped quote inside a value survives",
			line:        `time=2026-08-16T01:00:00.000-04:00 level=INFO msg="wrote tags" title="Some \"Quoted\" Book"`,
			wantMessage: "wrote tags",
			wantSummary: `wrote tags title=Some "Quoted" Book`,
			wantAttrs:   map[string]string{"title": `Some "Quoted" Book`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ParseLogLineFull(tt.line)

			assert.Equal(t, tt.wantMessage, p.Message,
				"the message must stop at its own closing quote")
			assert.Equal(t, tt.wantSummary, RenderSummary(p),
				"the summary must carry the data the message refers to")

			got := map[string]string{}
			for _, a := range p.Attrs {
				got[a.Key] = a.Value
			}
			assert.Equal(t, tt.wantAttrs, got)
		})
	}
}

// The op_id and component attrs must keep being lifted into their own fields —
// the batching key and the component: tag depend on them.
func TestParseLogLineFull_StillLiftsOpIDAndComponent(t *testing.T) {
	p := ParseLogLineFull(`time=2026-08-16T01:00:00.000-04:00 level=INFO msg="scan complete" op_id=op-9 component=scanner count=12`)
	assert.Equal(t, "op-9", p.OpID)
	assert.Equal(t, "scanner", p.Component)
}

// scanSlogAttrs is the piece that makes the rest correct; pin its edge cases
// directly rather than only through the parser.
func TestScanSlogAttrs(t *testing.T) {
	attrs := scanSlogAttrs(`time=T level=INFO msg="a b" path="/x/y z.mp3" n=3 empty= last=end`)
	require.Len(t, attrs, 7)
	assert.Equal(t, LogAttr{"msg", "a b"}, attrs[2], "a quoted value may contain spaces")
	assert.Equal(t, LogAttr{"path", "/x/y z.mp3"}, attrs[3])
	assert.Equal(t, LogAttr{"n", "3"}, attrs[4], "a bare value ends at the next space")
	assert.Equal(t, LogAttr{"empty", ""}, attrs[5])
	assert.Equal(t, LogAttr{"last", "end"}, attrs[6], "the final attr has no trailing space to stop at")
}

func TestEndsWithPreposition(t *testing.T) {
	for _, s := range []string{"cover art saved to", "enrichment succeeded for", "copied from", "wrote tags:"} {
		assert.True(t, endsWithPreposition(s), "%q is a dangling sentence", s)
	}
	for _, s := range []string{"tag writing failed", "scan complete", "", "toaster"} {
		assert.False(t, endsWithPreposition(s), "%q is a complete sentence", s)
	}
}
