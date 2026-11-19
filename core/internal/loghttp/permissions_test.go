// file: core/internal/loghttp/permissions_test.go
// version: 1.0.0
// guid: fcea5e18-897d-4f58-8688-b96d12f281ea
// last-edited: 2026-11-18

package loghttp

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
)

// TestJournal_RequiresSystemManage (the route half, 05-logging §10): the
// registered route needs core:system.manage and a principal holding it gets
// 200's body. The 403 and 401 cells are the router's stage 13, which arrives
// with F4-08; F5-18, which mounts the route after it, deletes the skips. The
// handler checks no permission itself (enforcement lives in one place).
func TestJournal_RequiresSystemManage(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	if err := Register(r.CoreGroup(), Deps{Journal: &fakeJournal{}}); err != nil {
		t.Fatal(err)
	}
	var routes []httpapi.RouteInfo
	for ri := range r.Routes() {
		routes = append(routes, ri)
	}
	if len(routes) != 1 {
		t.Fatalf("%d routes registered, want 1: %+v", len(routes), routes)
	}
	ri := routes[0]
	if ri.Method != http.MethodGet || ri.Pattern != "/api/core/v1/system/logs" || ri.Name != "listSystemLogs" ||
		ri.Owner != "core" || ri.Kind != httpapi.RouteKindNative || ri.RateClass != httpapi.RateDefault ||
		ri.Guards != (httpapi.Guards{}) {
		t.Errorf("route = %+v", ri)
	}
	if ri.Perm.String() != rbac.Need("core:system.manage").String() {
		t.Errorf("route needs %s, want core:system.manage", ri.Perm)
	}

	t.Run("system_manage_principal_gets_the_page", func(t *testing.T) {
		fj := &fakeJournal{page: log.JournalPage{Records: []log.Record{{Time: at(0), Level: log.LevelWarn, Module: "core", Message: "alarm"}}}}
		v := mustCall(t, newAPI(fj, nil), principal("u-bruce", "core:system.manage"), queryRequest{})
		if !slices.Equal(messages(v), []string{"alarm"}) {
			t.Errorf("records = %v", messages(v))
		}
		if r := v.Records[0]; r.Level != "warn" || r.Module != "core" || !r.Time.Equal(at(0)) || r.Attrs == nil {
			t.Errorf("record on the wire = %+v, want level warn (lower case), module core, its time, a non-nil attrs", r)
		}
	})

	// Stage-13 table: the router answers, the handler is never reached.
	for _, c := range []struct {
		name  string
		p     *auth.Principal
		want  int
		wantC string
	}{
		{"system_view_only_gets_403", principal("u-dick", "core:system.view"), http.StatusForbidden, "permission_denied"},
		{"no_principal_gets_401", nil, http.StatusUnauthorized, "authentication_required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
		})
	}

	// F6-05: the event half (no core.log.recorded.v1 events) is added here
}

// A books-style predicate (06 LogOtherUserData): a record naming selina is her
// data unless she is the viewer. It reads the message, so it classifies an
// imported record, which carries no attributes, as it does a new one.
func namesSelina(rec log.Record, viewer string) bool {
	return strings.Contains(strings.ToLower(rec.Message), "selina") && viewer != "u-selina"
}

func seedUserLines(t testing.TB, st *logjournal.Store) {
	t.Helper()
	ctx := context.Background()
	imported := []log.ImportedRecord{
		{SourceID: "syslog:0001", Time: at(1e9), Level: log.LevelInfo, Module: "books", Subsystem: "users",
			Message: "user selina renamed"},
		{SourceID: "syslog:0002", Time: at(2e9), Level: log.LevelInfo, Module: "books", Subsystem: "scanner",
			Message: "imported scan done", Attrs: []slog.Attr{slog.String("user_id", "u-dick")}},
	}
	if err := logjournal.NewImporter(st).Import(ctx, imported); err != nil {
		t.Fatal(err)
	}
	fresh := []log.Record{
		{Time: at(3e9), Level: log.LevelInfo, Module: "core", Subsystem: "users", Message: "selina's password was reset",
			Attrs: []slog.Attr{slog.String("user_id", "u-selina")}},
		{Time: at(4e9), Level: log.LevelInfo, Module: "core", Subsystem: "scanner", Message: "scan finished",
			Attrs: []slog.Attr{slog.String("user_id", "u-bruce"), slog.String("request_id", "req-1")}},
		{Time: at(5e9), Level: log.LevelInfo, Module: "core", Subsystem: "http", Message: "grouped attribute",
			Attrs: []slog.Attr{slog.Group("ctx", slog.String("user_id", "u-alfred"))}},
		{Time: at(6e9), Level: log.LevelInfo, Module: "core", Subsystem: "http", Message: "no user at all",
			Attrs: []slog.Attr{slog.Int("status", 204)}},
	}
	if err := st.Append(ctx, fresh); err != nil {
		t.Fatal(err)
	}
}

func byMessage(v pageView) map[string]recordView {
	m := map[string]recordView{}
	for _, r := range v.Records {
		m[r.Message] = r
	}
	return m
}

// TestJournal_OtherUsersLinesNeedUsersView (05-logging §10): over a journal of
// imported and new records, a system.manage principal without users.view gets
// neither the imported nor the new record the predicate flags and sees user_id
// blank on the rest; with users.view it gets every record with user_id intact.
func TestJournal_OtherUsersLinesNeedUsersView(t *testing.T) {
	st := openJournal(t)
	seedUserLines(t, st)
	a := newAPI(st, namesSelina)

	t.Run("without_users_view", func(t *testing.T) {
		v := mustCall(t, a, principal("u-bruce", permManage), queryRequest{})
		got := byMessage(v)
		for _, flagged := range []string{"user selina renamed", "selina's password was reset"} {
			if _, ok := got[flagged]; ok {
				t.Errorf("%q (flagged) was served", flagged)
			}
		}
		if len(v.Records) != 4 {
			t.Fatalf("%d records %v, want 4", len(v.Records), messages(v))
		}
		for msg, key := range map[string]string{"imported scan done": "user_id", "scan finished": "user_id", "grouped attribute": "ctx.user_id"} {
			val, ok := got[msg].Attrs[key]
			if !ok || val != "" {
				t.Errorf("%q: %s = %q (present %v), want present and blank", msg, key, val, ok)
			}
		}
		if got["scan finished"].Attrs["request_id"] != "req-1" {
			t.Errorf("request_id was touched: %v", got["scan finished"].Attrs)
		}
		if _, ok := got["no user at all"].Attrs["user_id"]; ok || got["no user at all"].Attrs["status"] != "204" {
			t.Errorf("a record with no user_id changed: %v", got["no user at all"].Attrs)
		}
	})

	t.Run("with_users_view", func(t *testing.T) {
		v := mustCall(t, a, principal("u-bruce", permManage, permUsers), queryRequest{})
		if len(v.Records) != 6 {
			t.Fatalf("%d records %v, want all 6", len(v.Records), messages(v))
		}
		got := byMessage(v)
		if got["imported scan done"].Attrs["user_id"] != "u-dick" || got["scan finished"].Attrs["user_id"] != "u-bruce" ||
			got["grouped attribute"].Attrs["ctx.user_id"] != "u-alfred" || got["selina's password was reset"].Attrs["user_id"] != "u-selina" {
			t.Errorf("user_id changed for a viewer holding users.view: %+v", v.Records)
		}
	})

	t.Run("the_subject_herself_still_needs_users_view_for_flagged_to_pass", func(t *testing.T) {
		// The predicate sees the viewer's id: selina is not "another user" to herself.
		v := mustCall(t, a, principal("u-selina", permManage), queryRequest{})
		if _, ok := byMessage(v)["selina's password was reset"]; !ok {
			t.Errorf("the viewer's own flagged-for-others line was withheld: %v", messages(v))
		}
	})

	t.Run("no_predicate_registered_blanks_without_omitting", func(t *testing.T) {
		v := mustCall(t, newAPI(st, nil), principal("u-bruce", permManage), queryRequest{})
		if len(v.Records) != 6 {
			t.Fatalf("%d records, want 6 (nothing to flag)", len(v.Records))
		}
		for _, r := range v.Records {
			for k, val := range r.Attrs {
				if (k == "user_id" || strings.HasSuffix(k, ".user_id")) && val != "" {
					t.Errorf("%q: %s = %q not blanked", r.Message, k, val)
				}
			}
		}
	})

	t.Run("the_store_is_not_modified", func(t *testing.T) {
		mustCall(t, a, principal("u-bruce", permManage), queryRequest{})
		p, err := st.Query(context.Background(), log.JournalQuery{MinLevel: log.LevelTrace, Limit: 1000})
		if err != nil || len(p.Records) != 6 {
			t.Fatalf("store: %d records, %v", len(p.Records), err)
		}
		for _, r := range p.Records {
			if r.Message == "scan finished" && r.Attrs[0].Value.String() != "u-bruce" {
				t.Errorf("blanking wrote through to the store: %v", r.Attrs)
			}
		}
	})
}
