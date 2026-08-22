// file: internal/server/handlers/abs/stats_test.go
// version: 1.0.1
// guid: 0a6d29c8-51b7-4e30-bf94-8c73e105ad62
// last-edited: 2026-08-22

package abs_test

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// The listening-stats family used to answer 404 on purpose (spec §1.8.6). That was
// wrong in a way `try?` hides: AudioBooth's NetworkService sets the server's
// connection status on EVERY response —
//
//	guard 200...299 ~= httpResponse.statusCode else { ... updateStatus(.connectionError) }
//	await updateStatus(.connected)
//
// — so any non-2xx flips the home-screen dot orange and the next 2xx flips it back.
// /api/me/listening-stats is fetched on every home refresh, which is precisely the
// owner-reported "it still turns orange randomly".
//
// The tests below therefore assert TWO things per endpoint: the status is 2xx (the
// indicator stays green) and every field the client's decoder requires is present
// with the right JSON type. A field-complete body behind a 404 would be useless, and
// a 200 with a missing field throws in Swift's all-or-nothing decode.

// requireNum asserts a JSON number is present at key.
func requireNum(t *testing.T, body map[string]any, key string) float64 {
	t.Helper()
	v, ok := body[key]
	if !ok {
		t.Fatalf("required field %q is ABSENT — the client's decode throws on this", key)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("field %q = %#v, want a JSON number", key, v)
	}
	return n
}

// requireArray asserts a non-null JSON array is present at key.
func requireArray(t *testing.T, body map[string]any, key string) []any {
	t.Helper()
	v, ok := body[key]
	if !ok {
		t.Fatalf("required field %q is ABSENT", key)
	}
	arr, ok := v.([]any)
	if !ok || arr == nil {
		t.Fatalf("field %q = %#v, want a non-null JSON array", key, v)
	}
	return arr
}

// requireObject asserts a non-null JSON object is present at key.
func requireObject(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := body[key]
	if !ok {
		t.Fatalf("required field %q is ABSENT", key)
	}
	obj, ok := v.(map[string]any)
	if !ok || obj == nil {
		t.Fatalf("field %q = %#v, want a non-null JSON object", key, v)
	}
	return obj
}

// 🔴 TestListeningStats_Is200SoTheConnectionDotStaysGreen is the whole point.
func TestListeningStats_Is200SoTheConnectionDotStaysGreen(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodGet, "/api/me/listening-stats", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s — any non-2xx flips AudioBooth's connection indicator orange", code, raw)
	}
	// ListeningStats' four required fields. recentSessions and items are optional.
	requireNum(t, body, "totalTime")
	requireNum(t, body, "today")
	requireObject(t, body, "days")
	requireObject(t, body, "dayOfWeek")
}

// TestListeningStats_ReportsRealListenedTime — the total is substantiated from the
// per-book state the playback sync maintains, not a placeholder.
func TestListeningStats_ReportsRealListenedTime(t *testing.T) {
	w := newWriteHarness(t)

	code, play, raw := w.req(t, http.MethodPost, "/api/items/"+w.syncID+"/play", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("play = %d %s", code, raw)
	}
	sessionID, _ := play["id"].(string)
	if code, _, raw := w.req(t, http.MethodPost, "/api/session/"+sessionID+"/sync",
		map[string]any{"currentTime": 900.0, "timeListened": 450.0}); code != http.StatusOK {
		t.Fatalf("sync = %d %s", code, raw)
	}

	_, body, _ := w.req(t, http.MethodGet, "/api/me/listening-stats", nil)
	if got := requireNum(t, body, "totalTime"); got != 450 {
		t.Fatalf("totalTime = %v, want 450 — the listened delta from the sync", got)
	}
}

// TestListeningStats_StaysGreenWhenTheStoreFails: failing with a 5xx here would trip
// the SAME orange indicator this endpoint exists to keep green, so a read failure
// reports 0 instead.
func TestListeningStats_StaysGreenWhenTheStoreFails(t *testing.T) {
	seed := seedOracleLibrary(t)
	broken := &fakeUserData{}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(broken))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	broken.err = errString("store down")

	rec, body := h.do(t, request{method: http.MethodGet, path: "/api/me/listening-stats", headers: bearer(tok)})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — a 5xx turns the dot orange just like a 404 did", rec.Code)
	}
	if got := requireNum(t, body, "totalTime"); got != 0 {
		t.Fatalf("totalTime = %v, want 0 when the store is unreadable", got)
	}
}

// TestListeningStats_ReadFailureLogsAndCountsButReturns200: on a read failure,
// assert that the response is still 200 with totalTime:0 (keeping the connection
// indicator green), and assert that the failure was logged and counted in the metric.
func TestListeningStats_ReadFailureLogsAndCountsButReturns200(t *testing.T) {
	metrics.Register() // idempotent (sync.Once); the counter must be registered to be gathered
	seed := seedOracleLibrary(t)
	broken := &fakeUserData{}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(broken))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	broken.err = errString("listening seconds read failed")

	before := gatherABSListeningStatsFailureCount(t)
	rec, body := h.do(t, request{method: http.MethodGet, path: "/api/me/listening-stats", headers: bearer(tok)})
	after := gatherABSListeningStatsFailureCount(t)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — a 5xx turns the dot orange just like a 404 did", rec.Code)
	}
	if got := requireNum(t, body, "totalTime"); got != 0 {
		t.Fatalf("totalTime = %v, want 0 when the read fails", got)
	}
	if after != before+1 {
		t.Fatalf("abs_listening_stats_read_failures counter went %v -> %v, want +1: the failure metric is not wired", before, after)
	}
	if !metricFamilyPresent(t, "audiobook_organizer_abs_listening_stats_read_failures_total") {
		t.Fatal("metric family absent from the default registry — Register() does not include the ABS-N6 counter")
	}
}

// gatherABSListeningStatsFailureCount reads the ABS listening-stats read-failure
// counter from the DEFAULT registry — the same registry /metrics serves.
func gatherABSListeningStatsFailureCount(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "audiobook_organizer_abs_listening_stats_read_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue()
		}
		// Counter vec with no children (no label combinations yet)
		return 0
	}
	return 0
}

// metricFamilyPresent reports whether a metric family is present in the DEFAULT
// registry, used to verify that a counter was properly registered.
func metricFamilyPresent(t *testing.T, name string) bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() == name {
			return true
		}
	}
	return false
}

func TestListeningSessions_ConformsToTheClientDecoder(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodGet, "/api/me/listening-sessions?page=2&itemsPerPage=25", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	// ListeningHistoryResponse: all five required.
	requireNum(t, body, "total")
	requireNum(t, body, "numPages")
	requireArray(t, body, "sessions")
	if got := requireNum(t, body, "page"); got != 2 {
		t.Fatalf("page = %v, want the requested 2", got)
	}
	if got := requireNum(t, body, "itemsPerPage"); got != 25 {
		t.Fatalf("itemsPerPage = %v, want the requested 25", got)
	}
}

func TestItemListeningSessions_ConformsToTheClientDecoder(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodGet, "/api/me/item/listening-sessions/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	// This response has numPages/page/itemsPerPage/sessions — and NO total.
	requireNum(t, body, "numPages")
	requireNum(t, body, "page")
	requireNum(t, body, "itemsPerPage")
	requireArray(t, body, "sessions")
}

func TestYearStats_ConformsToTheClientDecoder(t *testing.T) {
	w := newWriteHarness(t)
	code, body, raw := w.req(t, http.MethodGet, "/api/me/stats/year/2026", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	for _, key := range []string{
		"totalListeningSessions", "totalListeningTime",
		"totalBookListeningTime", "totalPodcastListeningTime",
		"numBooksFinished", "numBooksListened",
	} {
		requireNum(t, body, key)
	}
	for _, key := range []string{"topAuthors", "topGenres", "booksWithCovers", "finishedBooksWithCovers"} {
		requireArray(t, body, key)
	}
}

// 🔴 TestItemListeningSessions_DoesNotShadowTheBookmarkRoutes. Both hang off
// /api/me/item/: one with a LITERAL "listening-sessions" segment, one with a :id
// wildcard. gin must route them independently — if the wildcard swallowed the
// literal, bookmarks would break, and if the literal swallowed the wildcard, a book
// whose id happened to collide would.
func TestItemListeningSessions_DoesNotShadowTheBookmarkRoutes(t *testing.T) {
	w := newWriteHarness(t)

	if code, _, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 30, "title": "still routable"}); code != http.StatusOK {
		t.Fatalf("bookmark create = %d %s — the stats route shadowed it", code, raw)
	}
	code, body, raw := w.req(t, http.MethodGet, "/api/me/item/listening-sessions/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("item listening-sessions = %d %s", code, raw)
	}
	if _, present := body["sessions"]; !present {
		t.Fatalf("item listening-sessions returned the wrong handler's body: %#v", body)
	}
}

// TestStatsEndpoints_RequireAuth — statistics are user-scoped; they must sit behind
// the same fail-closed middleware as the rest of /api/me.
func TestStatsEndpoints_RequireAuth(t *testing.T) {
	w := newWriteHarness(t)
	for _, path := range []string{
		"/api/me/listening-stats",
		"/api/me/listening-sessions",
		"/api/me/stats/year/2026",
		"/api/me/item/listening-sessions/" + w.syncID,
	} {
		rec, _ := w.do(t, request{method: http.MethodGet, path: path})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", path, rec.Code)
		}
	}
}
