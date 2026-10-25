// file: internal/activity/user_data_test.go
// version: 1.2.0
// guid: 70dbd475-4218-4a5d-ae68-86d5bd16ad4f
// last-edited: 2026-10-25

package activity

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// WithoutOthersUserData drops the trash purge's per-user summary and leaves
// every other detail, every other entry type, and the input entry alone.
func TestWithoutOthersUserData(t *testing.T) {
	in := database.ActivityEntry{Type: TypeTrashDiscardProgressPurge, Details: map[string]any{
		DetailProgressSummary: "invented-reader: finished", "users_cleared": 1,
	}}
	out := WithoutOthersUserData(in)
	assert.NotContains(t, out.Details, DetailProgressSummary)
	assert.Equal(t, 1, out.Details["users_cleared"])
	assert.Equal(t, "invented-reader: finished", in.Details[DetailProgressSummary], "the input is not modified")

	other := database.ActivityEntry{Type: "library.scan", Details: map[string]any{DetailProgressSummary: "kept"}}
	assert.Equal(t, other, WithoutOthersUserData(other))
	assert.Equal(t, database.ActivityEntry{Type: TypeTrashDiscardProgressPurge}, WithoutOthersUserData(database.ActivityEntry{Type: TypeTrashDiscardProgressPurge}))
}

// The server's own log lines are mirrored into the activity log, and several
// name a user. NamesOthersUserData must hold for the real shapes (the abs
// progress handler, the merge undo's warnings, the owner guard, the openid hop,
// the engine's owner-apply line) and must not hold for ordinary lines.
func TestNamesOthersUserData(t *testing.T) {
	sys := func(source, summary string, tags ...string) database.ActivityEntry {
		return database.ActivityEntry{Type: "system", Source: source, Summary: summary, Tags: tags}
	}
	for name, tc := range map[string]struct {
		e    database.ActivityEntry
		want bool
	}{
		"abs progress patch": {sys("abs", "progress patch: user=01INVENTEDUSER0000000000A item=li_1 currentTime=3600.5 isFinished=false"), true},
		"abs line without a user in it, still the abs source": {sys("abs", "library listing served"), true},
		"abs-cache source":            {sys("abs-cache", "refreshed 12 libraries"), true},
		"merge source":                {sys("merge", "survivor reconciled"), true},
		"merge undo warning":          {sys("revert", "revert of change c1 restored in part: user 01INVENTEDUSER0000000000A progress on b2 was written after the follow"), true},
		"owner guard":                 {sys("http", "owner action allowed: route=/x http_method=POST user=01INVENTEDUSER0000000000A access_email=bruce.wayne@example.invalid method=cookie"), true},
		"openid rejection":            {sys("abs", "openid browser hop rejected an assertion: reason=x status=403 email=bruce.wayne@example.invalid"), true},
		"config update":               {sys("config", "config update refused: method=cookie access_email=bruce.wayne@example.invalid"), true},
		"owner apply line":            {database.ActivityEntry{Type: "repairs.apply", Summary: "repairs.apply maintenance.audible-read-status: owner apply by user 01INVENTEDUSER0000000000A (cookie): 3 of 3 owner row(s) written"}, true},
		"tagged by the writer":        {sys("scheduler", "something with no recognisable shape", TagUserData), true},
		"source tag on a digest item": {sys("", "plain words", "source:abs"), true},

		"scheduler line":       {sys("scheduler", "Next sync in 28m"), false},
		"scan line":            {sys("scanner", "scan complete count=12"), false},
		"the word user alone":  {sys("server", "the user should confirm the interface"), false},
		"non-system abs-named": {database.ActivityEntry{Type: "library.scan", Source: "abs", Summary: "imported 4 books"}, false},
		"empty":                {database.ActivityEntry{}, false},
	} {
		assert.Equal(t, tc.want, NamesOthersUserData(tc.e), name)
	}
}

// Writer stamps TagUserData on a line that names a user, from its text or from
// the key of one of its attrs, and leaves other lines untagged.
func TestWriter_TagsLinesThatNameAUser(t *testing.T) {
	store := newTestPebbleActivityStore(t)
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devNull.Close()

	w := NewWriter(store, 50)
	w.stdout = devNull
	for _, line := range []string{
		"2026/03/25 17:35:08 logger.go:103: [info] abs: progress patch: user=01INVENTEDUSER0000000000A currentTime=12.5",
		`time=2026-08-16T01:00:00.000-04:00 level=INFO msg="owner action allowed" user_id=01INVENTEDUSER0000000000A`,
		"2026/03/25 17:35:08 logger.go:103: [info] scheduler: Next sync in 28m",
	} {
		fmt.Fprintln(w, line)
	}
	w.Flush()

	entries, _, err := store.Query(context.Background(), database.ActivityFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, entries, 3)
	tagged := 0
	for _, e := range entries {
		has := false
		for _, tag := range e.Tags {
			has = has || tag == TagUserData
		}
		if e.Source == "scheduler" {
			assert.False(t, has, "an ordinary line is not tagged")
			continue
		}
		assert.True(t, has, "the line %q names a user and is tagged", e.Summary)
		tagged++
	}
	assert.Equal(t, 2, tagged)
}

// A line that names a user and is batched (LogBatch, the path the scanner's
// per-file lines take) keeps the tag: it batches under its own key, apart from
// the lines that name nobody, and the flushed entry is tagged. Before this the
// batch dropped the flag and the lines came back untagged to every viewer.
func TestLogBatch_UserDataLinesBatchApartAndStayTagged(t *testing.T) {
	w := newTestWriter(16)
	LogBatch(w, "op-b", "tag-scan", "scanner", BatchItem{Name: "progress patch: user=01INVENTEDUSER0000000000A", Count: 1})
	LogBatch(w, "op-b", "tag-scan", "scanner", BatchItem{Name: "/lib/Gotham/year-one.m4b", Count: 1})
	LogBatch(w, "op-b", "tag-scan", "scanner", BatchItem{Name: "/lib/Gotham/hush.m4b", Count: 1, Detail: "email=bruce.wayne@example.invalid"})
	LogBatch(w, "op-b", "tag-scan", "scanner", BatchItem{Name: "/lib/Gotham/knightfall.m4b", Count: 1})

	w.batcher.mu.Lock()
	assert.Len(t, w.batcher.pending, 2, "one batch for the user lines, one for the rest")
	w.batcher.mu.Unlock()

	FlushOperation(w, "op-b")
	entries := drainAll(w.ch, 5, 100*time.Millisecond)
	require.Len(t, entries, 2)
	var tagged, plain *database.ActivityEntry
	for i := range entries {
		if NamesOthersUserData(entries[i]) {
			tagged = &entries[i]
		} else {
			plain = &entries[i]
		}
	}
	require.NotNil(t, tagged, "the batch of user lines is tagged %s", TagUserData)
	require.NotNil(t, plain, "an untagged line still batches, and stays visible")
	assert.Contains(t, tagged.Tags, TagUserData)
	assert.EqualValues(t, 2, tagged.Details["original_count"])
	assert.NotContains(t, plain.Tags, TagUserData)
	assert.EqualValues(t, 2, plain.Details["original_count"], "the two plain lines coalesced")
}
