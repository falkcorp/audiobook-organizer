// file: internal/activity/user_data_test.go
// version: 1.10.0
// guid: 70dbd475-4218-4a5d-ae68-86d5bd16ad4f
// last-edited: 2026-10-26

package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

// The audit rows that name the person who acted (the trash discard-and-purge,
// the admin debug edit and undo) lose the actor's username for a caller who may
// not manage users, and keep the row and every other detail.
func TestWithoutOthersUserData_StripsTheActorName(t *testing.T) {
	for _, typ := range []string{TypeTrashDiscardProgressPurge, TypeAdminDebugEdit, TypeAdminDebugUndo} {
		in := database.ActivityEntry{Type: typ, Tier: "audit", Summary: "a book changed", Details: map[string]any{
			DetailActor: "bruce.wayne (01INVENTEDUSER0000000000A)", "book_id": "book-invented-year-one",
		}}
		out := WithoutOthersUserData(in)
		assert.NotContains(t, out.Details, DetailActor, typ)
		assert.Equal(t, "book-invented-year-one", out.Details["book_id"], typ)
		assert.Equal(t, "a book changed", out.Summary, typ)
		assert.Contains(t, in.Details, DetailActor, "%s: the input is not modified", typ)
		assert.False(t, NamesOthersUserData(database.ActivityEntry{Type: typ, Summary: "a book changed"}),
			"%s: the row itself is about a book and stays visible", typ)
	}
}

// The account and identity audit rows are withheld by their type: their summary
// says "account <id>" and their details carry account ids and provider error
// text, which no wording rule can own.
func TestNamesOthersUserData_AccountAuditTypes(t *testing.T) {
	for _, typ := range []string{
		TypeAuthEmailDisplaced, TypeUserMerge,
		TypeGuestServiceTokenPrefix + "minted", TypeGuestServiceTokenPrefix + "mint_failed",
		TypeGuestServiceTokenPrefix + "revoked", TypeGuestServiceTokenPrefix + "revoke_failed",
	} {
		e := database.ActivityEntry{Type: typ, Tier: "audit", Source: "invite", Summary: "a thing happened to somebody"}
		assert.True(t, NamesOthersUserData(e), typ)
	}
	assert.False(t, NamesOthersUserData(database.ActivityEntry{Type: "metadata_apply", Tier: "audit", Summary: "a thing happened"}))
}

// Every line the review of PR 4166 found missing, rendered the way the server
// really emits it: slog's text handler writes `msg="<subsystem>: <text>"`, so
// the line parses to Source "server" and the subsystem survives only as the
// summary's prefix. Each is withheld; the ordinary lines are not.
func TestWriter_RealTextHandlerLinesNamingUsersAreWithheld(t *testing.T) {
	const (
		u1 = "01INVENTEDUSER0000000000A"
		u2 = "01INVENTEDUSER0000000000B"
		b1 = "01INVENTEDBOOK00000000YR1"
	)
	store := newTestPebbleActivityStore(t)
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devNull.Close()
	w := NewWriter(store, 50)
	w.stdout = devNull
	log := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))

	hidden := map[string]func(){
		"playback": func() {
			log.Warn("database.playback: SetUserBookState " + u1 + "/" + b1 + ": stored state unreadable (bad json); overwriting it")
		},
		"verified mail": func() {
			log.Warn("database.auth: verified-email lookup: an address is proven for more than one user (" + u1 + ", " + u2 + "); left unverified for both")
		},
		"invite accept": func() {
			log.Warn("server.invites: invite accept: an emailed invitation took its address back from unverified account(s) [" + u1 + "]; new account " + u2)
		},
		"oauth resolve": func() {
			log.Warn("oauth.resolve: oauth resolve: verified google login took its email back from unverified account(s) [" + u1 + "]; new account " + u2)
		},
		"guest token": func() {
			log.Info("server.invites: guest service token: minted and added to the books policy for account " + u1 + " (token tok-1)")
		},
		"collections": func() {
			log.Warn("collections: collection col-1 has owner \"" + u1 + "\" which is not a user; first admin " + u2 + " stands in for it")
		},
		"internal err": func() {
			log.Error("revert failed", "err", "store cannot restore user listening state; "+u1+" on "+b1+" left as the import wrote it")
		},
		"bootstrap": func() {
			log.Info("Token consumed new API key created user key_id ip", "adminUser", "bruce.wayne", "created", u1)
		},
		"email only": func() { log.Info("sign-in refused for bruce.wayne@example.invalid") },
		// The itunes playlist import's completion line renders the account that
		// owns the imported playlists as a bare key=value pair with no user word
		// after "owner".
		"playlist import owner": func() {
			log.Info("itunes-playlist-import complete", "mode", "APPLY", "smartFound", 3, "owner", u1)
		},
		"playlist import owner user id": func() {
			log.Info("itunes-playlist-import complete", "mode", "APPLY", "smartFound", 3, "owner_user_id", u1)
		},
	}
	visible := map[string]func(){
		"scheduler": func() { log.Info("scheduler: Next sync in 28m") },
		"scan":      func() { log.Info("scanner: scan complete count=12 path=/lib/Gotham/year-one.m4b") },
		"book id":   func() { log.Info("organizer: moved book " + b1 + " to /lib/Gotham/hush.m4b") },
	}
	for _, f := range hidden {
		f()
	}
	for _, f := range visible {
		f()
	}
	w.Flush()

	entries, _, err := store.Query(context.Background(), database.ActivityFilter{Limit: 50})
	require.NoError(t, err)
	require.Len(t, entries, len(hidden)+len(visible))
	withheld, shown := 0, 0
	for _, e := range entries {
		assert.Equal(t, "server", e.Source, "a text-handler line has no source of its own: %q", e.Summary)
		isVisible := strings.HasPrefix(e.Summary, "scheduler:") || strings.HasPrefix(e.Summary, "scanner:") || strings.HasPrefix(e.Summary, "organizer:")
		if isVisible {
			assert.False(t, NamesOthersUserData(e), "an ordinary line stays visible: %q", e.Summary)
			shown++
			continue
		}
		assert.True(t, NamesOthersUserData(e), "the line names a user and is withheld: %q", e.Summary)
		withheld++
	}
	assert.Equal(t, len(hidden), withheld)
	assert.Equal(t, len(visible), shown)
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

// NamesOthersUserLog is the one predicate for a server log line: the activity
// filter's "system" branch and the diagnostics export both use it, so a line of a
// user-data subsystem is withheld by source, by message prefix, or by its text,
// and an ordinary line passes.
func TestNamesOthersUserLog(t *testing.T) {
	cases := []struct {
		name, source, message string
		want                  bool
	}{
		{"user-data source, no user text", "abs", "seek to Year One chapter 3", true},
		{"user-data source prefix", "abs-cache", "evicted Hush", true},
		{"message prefix under the server source", "server", "merge: moved 3 rows for Knightfall", true},
		{"user text under an ordinary source", "scanner", "progress patch: user=01INVENTEDUSER0000000000A", true},
		{"ordinary line", "scanner", "scan complete count=12", false},
		{"ordinary prefix", "server", "scanner: scan complete count=12", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NamesOthersUserLog(tc.source, tc.message))
		})
	}
}

// WithoutUserAttrs: a log line's message can be clean while its attributes name
// a user. Keys that name one go, at any depth and in lists, and so does a value
// whose text does; the rest is kept, and an empty or non-JSON input is safe.
func TestWithoutUserAttrs(t *testing.T) {
	cleaned := WithoutUserAttrs(`{"user_id":"01INVENTEDUSER0000000000A","target_user_id":"x","Username":"bruce_wayne",` +
		`"email":"bruce.wayne@example.invalid","actor":"selina_kyle","account":"a","book":"Year One",` +
		`"note":"progress for user 01INVENTEDUSER0000000000B","count":3,` +
		`"nested":{"owner_email":"b@example.invalid","title":"Hush"},` +
		`"list":[{"login":"x","title":"Knightfall"},"user 01INVENTEDUSER0000000000C","plain"]}`)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(cleaned), &got))
	assert.Equal(t, map[string]any{
		"book":   "Year One",
		"count":  float64(3),
		"nested": map[string]any{"title": "Hush"},
		"list":   []any{map[string]any{"title": "Knightfall"}, "plain"},
	}, got)

	assert.Equal(t, "", WithoutUserAttrs(""))
	assert.Equal(t, "{}", WithoutUserAttrs("{}"))
	assert.Equal(t, "{}", WithoutUserAttrs("not json, email bruce.wayne@example.invalid"))
	assert.Equal(t, "plain text", WithoutUserAttrs("plain text"))
}

// The ids a log line carries under a key that names an account, in every shape a
// writer renders them: key=value, key: value, a bracketed list, a quoted JSON
// pair. Each is withheld, though the line has no "user <id>" sentence in it; the
// same shapes with a book id, a guid or an ordinary word are not.
func TestNamesUserText_IdOnlyShapes(t *testing.T) {
	const (
		u    = "01INVENTEDUSER0000000000A"
		book = "01INVENTEDBOOK00000000YR1"
	)
	cases := []struct {
		name, text string
		want       bool
	}{
		{"owner=", "itunes-playlist-import complete mode=APPLY owner=" + u, true},
		{"owner_user_id=", "complete mode=APPLY owner_user_id=" + u, true},
		{"account_id=", "recorded account_id=" + u + " count=3", true},
		{"account:", "recorded account: " + u, true},
		{"actor=", "undo by actor=" + u, true},
		{"uid=", "progress uid=" + u, true},
		{"user_ids list", "merged user_ids=[" + u + " " + u + "]", true},
		{"users list", "kept users=[" + u + "]", true},
		{"survivor_id=", "merge survivor_id=" + u + " absorbed_id=" + book, true},
		{"absorbed_id=", "merge absorbed_id=" + u, true},
		{"JSON pair", `payload {"target_user_id":"` + u + `","book":"Year One"}`, true},
		{"JSON pair with a space", `payload {"owner_user_id": "` + u + `"}`, true},
		{"mixed case key", "Owner=" + u, true},
		{"sentence form", "first admin " + u + " stands in", true},
		{"quoted owner", `collection col-1 has owner "` + u + `"`, true},

		{"book id", "moved book_id=" + book + " to /lib/Gotham/hush.m4b", false},
		{"guid", "file guid=" + u, false},
		{"owner of a file", "chown owner=root group=wheel", false},
		{"uid word", "uid mapping loaded for Knightfall", false},
		{"short id", "owner=b1", false},
		{"ordinary", "scan complete count=12", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NamesUserText(tc.text), tc.text)
		})
	}
}

// userAttrKey and userTextPattern read one list of key fragments, so an attribute
// key that is dropped from a line's attributes is also what the rendered text is
// withheld on, and uid is a token, not a fragment.
func TestUserAttrKey_UidIsATokenAndTheListIsShared(t *testing.T) {
	for _, k := range []string{"uid", "UIDs", "owner_uid", "survivor_id", "absorbed_id", "account_id", "target_user_id"} {
		assert.True(t, userAttrKey(k), k)
	}
	for _, k := range []string{"guid", "book_id", "path", "uidless"} {
		assert.False(t, userAttrKey(k), k)
	}
	const u = "01INVENTEDUSER0000000000A"
	for _, part := range userAttrKeyParts {
		assert.True(t, NamesUserText("x "+part+"_id="+u), part)
	}
}

// The subsystem name is compared case-insensitively and "abs." is a prefix like
// "abs-": a line whose logger is "ABS" or "abs.progress" is a user-data line.
func TestNamesOthersUserLog_SubsystemNamesAreCaseInsensitiveAndDotPrefixed(t *testing.T) {
	assert.True(t, NamesOthersUserLog("server", "ABS: seek to Year One chapter 3"))
	assert.True(t, NamesOthersUserLog("Abs", "seek to Year One chapter 3"))
	assert.True(t, NamesOthersUserLog("server", "abs.progress: seek to Year One chapter 3"))
	assert.True(t, NamesOthersUserLog("abs.progress", "seek to Year One chapter 3"))
	assert.True(t, NamesOthersUserLog("server", "Merge: moved 3 rows"))
	assert.False(t, NamesOthersUserLog("server", "absorb: scan complete"))
	assert.False(t, NamesOthersUserLog("server", "scanner: scan complete"))
}

// The activity store keeps a log line's attributes as Details, so the main
// activity page serves them unless they are cleaned as the operation log routes
// clean the same line: a row with a clean summary and a user id, an email or an
// owner among its Details loses those for a caller who may not manage users,
// at any depth, and keeps the rest: counts and flags under such a key
// ("users_cleared": 2) are not users. The input is not modified.
func TestWithoutOthersUserData_CleansEveryEntrysDetailsLikeAnOpLogLine(t *testing.T) {
	const u = "01INVENTEDUSER0000000000A"
	in := database.ActivityEntry{Type: "system", Summary: "itunes-playlist-import complete", Details: map[string]any{
		"owner": u, "user_id": u, "Email": "bruce.wayne@example.invalid", "book": "Year One", "count": 3,
		"users_cleared": 2, "owner_only": true, "uid": 1000, "account": 42, "userCount": 3, "owner_flag": true,
		"note":   "progress for user " + u,
		"nested": map[string]any{"account_id": u, "title": "Hush"},
		"list":   []any{map[string]any{"login": "x", "title": "Knightfall"}, "plain"},
	}}
	out := WithoutOthersUserData(in)
	assert.Equal(t, map[string]any{
		"book": "Year One", "count": 3, "users_cleared": 2, "owner_only": true,
		"nested": map[string]any{"title": "Hush"},
		"list":   []any{map[string]any{"title": "Knightfall"}, "plain"},
	}, out.Details)
	assert.Equal(t, "itunes-playlist-import complete", out.Summary)
	assert.Contains(t, in.Details, "owner", "the input is not modified")
	assert.Contains(t, in.Details["nested"], "account_id", "nor its nested maps")

	// The same cleaner serves a line's attributes: one set of rules, two readings.
	var viaAttrs map[string]any
	require.NoError(t, json.Unmarshal([]byte(WithoutUserAttrs(`{"owner":"`+u+`","book":"Year One"}`)), &viaAttrs))
	assert.Equal(t, map[string]any{"book": "Year One"}, viaAttrs)
}

// A number or a boolean under a user-naming key stays only when the key is one
// of the count and flag shapes (users_cleared, user_count, owner_only, ...): an
// identifying number, such as a numeric user id, is dropped like a string one,
// and so is a number under a key that merely ends in the letters "count".
func TestWithoutUserAttrs_NumbersUnderUserKeysStayOnlyAsCountsAndFlags(t *testing.T) {
	cleaned := WithoutUserAttrs(`{"user_id":1234567,"owner_id":42,"uid":1000,"account":42,"actor":7,` +
		`"target_user_id":5,"userCount":3,"owner_flag":true,"user_active":true,` +
		`"users_cleared":2,"user_count":12,"users_total":40,"owner_only":true,"num_users":3,"n_users":4,"count_users":5,` +
		`"book_id":99}`)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(cleaned), &got))
	assert.Equal(t, map[string]any{
		"users_cleared": float64(2), "user_count": float64(12), "users_total": float64(40), "owner_only": true,
		"num_users": float64(3), "n_users": float64(4), "count_users": float64(5), "book_id": float64(99),
	}, got)
}
