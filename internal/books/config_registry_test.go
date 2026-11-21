// file: internal/books/config_registry_test.go
// version: 1.0.0
// guid: 6c1d3f9a-7e24-4b58-a0c3-52d9e8b41f07
// last-edited: 2026-11-19

package books

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"log/slog"
	"maps"
	"math"
	"os"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The books half of F3-07's TestRenameTable_EveryKeyRegistered (05-config
// section 1.12.3, IMPL-1): core's half is core/kernel/rename_contract_test.go,
// which reads the rename table's projection onto core. This half reads the
// table itself, because books' sections are declared here and the table is
// the contract they must serve. The tests share one store over books' real
// Manifest.Settings with no layer, so every key holds its declared default.

const booksPrefix = "modules.books."

// booksRecordRows are the targets that name no key: a secret record per
// entry whose <id> exists only at runtime (05-config 1.12.3,
// modules.books.plugins.<id>.settings, "a secret record, no key"). Each is a
// reviewed row; a target with an <id> that is not listed here fails.
var booksRecordRows = map[string]cutover.TargetKind{
	"modules.books.plugins.<id>.settings": cutover.TargetSecret,
}

type booksStore struct {
	st   *config.Store
	keys map[config.Path]config.KeyState
}

func newBooksStore(t *testing.T) booksStore {
	t.Helper()
	ctx := context.Background()
	st, err := config.NewStore(config.Options{AppID: "bookskeys", Environ: []string{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	for _, s := range (&booksModule{}).Manifest().Settings {
		if _, err := s.Register(st.Registry()); err != nil {
			t.Fatalf("register %s: %v", s.Path(), err)
		}
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return booksStore{st: st, keys: maps.Collect(st.Keys())}
}

// stagingSink is the override store the importer stages through: the kernel
// hands over the native one (SetImportTarget); the oracle only needs Stage to
// accept what it is given, so nothing is kept.
type stagingSink struct{}

func (stagingSink) Load(context.Context) iter.Seq2[config.Override, error] {
	return func(func(config.Override, error) bool) {}
}

func (stagingSink) Stage(context.Context, *storage.Batch, []config.Override, []config.Path) error {
	return nil
}

// under reports a registered key at or below prefix.
func (b booksStore) under(prefix config.Path) []config.KeyState {
	var out []config.KeyState
	for k, ks := range b.keys {
		if k == prefix || strings.HasPrefix(string(k), string(prefix)+".") {
			out = append(out, ks)
		}
	}
	return out
}

// booksTarget is one books target of one rename-table row.
type booksTarget struct {
	head   string
	class  cutover.Class
	admin  bool
	target cutover.Target
	path   config.Path // Target.Path with a trailing ".*" removed
	whole  bool        // the row names a container ("<section>.*"): every key below it
}

// booksTargets is every key or secret target of the rename table under
// modules.books, in table order.
func booksTargets(t *testing.T) []booksTarget {
	t.Helper()
	var out []booksTarget
	for _, r := range cutover.Table().Rows {
		for _, tg := range r.Targets {
			if !strings.HasPrefix(tg.Path, booksPrefix) || (tg.Kind != cutover.TargetKey && tg.Kind != cutover.TargetSecret) {
				continue
			}
			p, whole := strings.CutSuffix(tg.Path, ".*")
			out = append(out, booksTarget{head: r.Head, class: r.Class, admin: r.Admin, target: tg, path: config.Path(p), whole: whole})
		}
	}
	if len(out) == 0 {
		t.Fatal("the rename table has no books target: the books tests would check nothing")
	}
	return out
}

// TestRenameTable_EveryKeyRegistered_Books: every books canonical path a class-k
// or class-s row of the rename table names is served by books' sections: a
// registered key, a container with keys below it (the "<section>.*" rows), or
// a reviewed record row. A class-k path is a plain key and a secret path is a
// secret key, so M2 stages each through the importer it will use.
func TestRenameTable_EveryKeyRegistered_Books(t *testing.T) {
	b := newBooksStore(t)
	seenRecord := map[string]bool{}
	checked := 0
	for _, bt := range booksTargets(t) {
		checked++
		if want, ok := booksRecordRows[string(bt.path)]; ok {
			seenRecord[string(bt.path)] = true
			if bt.target.Kind != want {
				t.Errorf("%s: the record row is a %s target, want %s", bt.path, bt.target.Kind, want)
			}
			if len(b.under(bt.path)) != 0 {
				t.Errorf("%s is a record row (no key) but a section declares keys at it", bt.path)
			}
			continue
		}
		if strings.Contains(string(bt.path), "<") {
			t.Errorf("%s names a runtime <id> and is not a reviewed record row", bt.path)
			continue
		}
		below := b.under(bt.path)
		if len(below) == 0 {
			t.Errorf("books path %s (HEAD %s) is a rename-table target but no books section declares it", bt.path, bt.head)
			continue
		}
		if bt.whole {
			continue
		}
		ks, ok := b.keys[bt.path]
		switch {
		case !ok:
			t.Errorf("%s (HEAD %s) is a container in the registry, but its row names one key", bt.path, bt.head)
		case bt.target.Kind == cutover.TargetSecret && ks.Secret == nil:
			t.Errorf("%s (HEAD %s) is a secret target but a plain key in the registry", bt.path, bt.head)
		case bt.target.Kind == cutover.TargetKey && ks.Secret != nil:
			t.Errorf("%s (HEAD %s) is a key target but a secret in the registry", bt.path, bt.head)
		}
	}
	for p := range booksRecordRows {
		if !seenRecord[p] {
			t.Errorf("record row %s is listed but no rename-table row targets it", p)
		}
	}
	t.Logf("%d books targets checked against %d registered keys", checked, len(b.keys))
}

// TestBooksSettings_KeysAreOnlyBooks: every key the books sections declare is
// under modules.books, and the one section of each path is declared once:
// the P1 manifest check (module.SettingsProblems) finds nothing.
func TestBooksSettings_KeysAreOnlyBooks(t *testing.T) {
	m := (&booksModule{}).Manifest()
	if probs := module.SettingsProblems(m); len(probs) != 0 {
		t.Fatalf("books' manifest settings have problems: %v", probs)
	}
	for p := range newBooksStore(t).keys {
		if !strings.HasPrefix(string(p), booksPrefix) {
			t.Errorf("books declares %s outside modules.books", p)
		}
	}
}

// headDefaults is the frozen HEAD viper default table, read straight from the
// fixture so the oracle shares no code with the sections it checks.
func headDefaults(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("cutover/testdata/head_defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "_header")
	// The two defaults HEAD computes (registerConfigDefaults and the
	// --playlists flag default) and the table cannot hold.
	m["concurrent_scans"] = float64(max(runtime.NumCPU(), 4))
	m["playlist_dir"] = "playlists"
	return m
}

// scheduleInput is HEAD's default scheduled entry as the blob would hold it,
// the input of the row's schedule converter.
func scheduleInput(t *testing.T, defaults map[string]any, head string) json.RawMessage {
	t.Helper()
	obj := map[string]any{}
	for _, f := range []string{"enabled", "interval", "on_startup", "period_hours"} {
		if v, ok := defaults[head+"."+f]; ok {
			obj[f] = v
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// jsonEqual compares two decoded JSON values, numbers by value.
func jsonEqual(a, b any) bool {
	an, aok := a.(float64)
	bn, bok := b.(float64)
	if aok && bok {
		return math.Abs(an-bn) < 1e-12
	}
	return reflect.DeepEqual(a, b)
}

// zeroJSON reports a decoded JSON value that is a type's zero: false, 0, "",
// null, an empty list or an empty object.
func zeroJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case float64:
		return x == 0
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// TestBooksSettings_DefaultsMatchHEAD: every class-k books key's declared
// default is what M2 would compute from HEAD's default for the row: HEAD's
// registered default (or its two computed ones), through the row's converter
// for a schedule. HEAD registers "" for an environment-bound key whose type is
// not a string (cf_guest_service_tokens, whisper_endpoints); viper reads that
// as the type's zero, so the key's default must be the zero. A key HEAD has
// no default for must be the zero too.
func TestBooksSettings_DefaultsMatchHEAD(t *testing.T) {
	b := newBooksStore(t)
	defaults := headDefaults(t)
	imp := b.st.Importer()
	exact, zero, converted := 0, 0, 0
	for _, bt := range booksTargets(t) {
		if bt.whole || bt.class != cutover.ClassKey && bt.class != cutover.ClassEnv {
			continue
		}
		gotRaw, err := imp.Default(bt.path)
		if err != nil {
			t.Errorf("%s: no registered default: %v", bt.path, err)
			continue
		}
		var got any
		if err := json.Unmarshal(gotRaw, &got); err != nil {
			t.Fatal(err)
		}
		var want any
		switch {
		case bt.target.Convert != "":
			out, _, err := cutover.Convert(bt.target, scheduleInput(t, defaults, bt.head))
			if err != nil {
				t.Errorf("%s: convert %s of HEAD's default: %v", bt.path, bt.target.Convert, err)
				continue
			}
			if err := json.Unmarshal(out, &want); err != nil {
				t.Fatal(err)
			}
			// The converter writes the fields HEAD's entry holds plus the
			// target's window placement; the key's default is the whole
			// ScheduleConfig, whose time of day and cron line are zero, and
			// whose placement must be the target's (compared below).
			g, _ := got.(map[string]any)
			w, _ := want.(map[string]any)
			for _, f := range []string{"daily_at", "cron"} {
				if v, ok := g[f]; ok && !zeroJSON(v) {
					t.Errorf("%s: default sets %s=%v, HEAD's entry has no such field", bt.path, f, v)
				}
			}
			for _, f := range []string{"window", "order"} {
				if v, ok := g[f]; ok && zeroJSON(v) {
					delete(g, f)
				}
			}
			if _, ok := w["every"]; !ok {
				delete(g, "every")
			}
			got = g
			converted++
		default:
			if v, ok := defaults[bt.head]; ok && !(v == "" && !isString(got)) {
				want = v
				exact++
			} else {
				want = nil
				if !zeroJSON(got) {
					t.Errorf("%s (HEAD %s): HEAD has no default (or the empty string for a non-string key), so the key's must be the zero; got %s", bt.path, bt.head, gotRaw)
				}
				zero++
				continue
			}
		}
		if !deepJSONEqual(got, want) {
			t.Errorf("%s (HEAD %s): default %s, HEAD's is %v", bt.path, bt.head, gotRaw, want)
		}
	}
	if exact == 0 || converted == 0 || zero == 0 {
		t.Fatalf("a branch of the oracle ran on nothing: exact=%d converted=%d zero=%d", exact, converted, zero)
	}
	t.Logf("defaults: %d equal to HEAD's, %d through a schedule converter, %d zero", exact, converted, zero)
}

func isString(v any) bool { _, ok := v.(string); return ok }

// deepJSONEqual is jsonEqual over nested lists and objects.
func deepJSONEqual(a, b any) bool {
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !deepJSONEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !deepJSONEqual(v, w) {
				return false
			}
		}
		return true
	}
	return jsonEqual(a, b)
}

// probeValue is a valid value of the same JSON type as def and different from
// it, or nil when the type has no cheap probe (a list of objects).
func probeValue(def any) json.RawMessage {
	switch x := def.(type) {
	case bool:
		return mustMarshal(!x)
	case float64:
		return mustMarshal(x + 1)
	case string:
		return mustMarshal(x + "x")
	case nil:
		return mustMarshal(0.5)
	case []any:
		if len(x) == 0 {
			return mustMarshal([]string{"probe"})
		}
	case map[string]any:
		if len(x) == 0 {
			return mustMarshal(map[string]any{})
		}
	}
	return nil
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// TestBooksSettings_StagedValuesDecode: M2's step for every class-k books key
// (Importer.Stage, the call F3-07 slice d makes) accepts a value of the key's
// own type, and a schedule key accepts what its converter writes. The probe
// differs from the default, because a value equal to the default stages
// nothing (C4) and would hide a type mismatch. The keys that cannot be probed
// by type (a list of objects, whisper_endpoints and the dedup ladders) are
// named, so adding one is a visible decision.
func TestBooksSettings_StagedValuesDecode(t *testing.T) {
	b := newBooksStore(t)
	b.st.SetImportTarget(stagingSink{})
	imp := b.st.Importer()
	unprobed := map[config.Path]bool{"modules.books.whisper.endpoints": true}
	staged, skipped := 0, 0
	for _, bt := range booksTargets(t) {
		if bt.whole || bt.class != cutover.ClassKey && bt.class != cutover.ClassEnv || bt.target.Kind != cutover.TargetKey {
			continue
		}
		var v json.RawMessage
		if bt.target.Convert != "" {
			in := json.RawMessage(`{"enabled":true,"interval":90,"period_hours":6}`)
			out, _, err := cutover.Convert(bt.target, in)
			if err != nil {
				t.Errorf("%s: convert %s: %v", bt.path, bt.target.Convert, err)
				continue
			}
			v = out
		} else {
			defRaw, err := imp.Default(bt.path)
			if err != nil {
				t.Errorf("%s: %v", bt.path, err)
				continue
			}
			var def any
			_ = json.Unmarshal(defRaw, &def)
			if v = probeValue(def); v == nil || unprobed[bt.path] {
				skipped++
				continue
			}
		}
		if err := imp.Stage(new(storage.Batch), bt.path, v, "cutover:m2"); err != nil {
			t.Errorf("M2 cannot stage %s (HEAD %s) with %s: %v", bt.path, bt.head, v, err)
			continue
		}
		staged++
	}
	if staged < 150 {
		t.Fatalf("only %d keys were staged (%d skipped): the oracle lost its coverage", staged, skipped)
	}
	t.Logf("%d keys staged, %d named as unprobed", staged, skipped)
}

// TestBooksSettings_OperatorAndSecretKeysRefuseStaging: the two operator-only
// secrets and every secret key are refused by the importer, so M2 can only
// ever move them through secrets.Importer, and a stored database copy never
// yields the ABS signing secret (05-config 1.3.7).
func TestBooksSettings_OperatorAndSecretKeysRefuseStaging(t *testing.T) {
	b := newBooksStore(t)
	imp := b.st.Importer()
	refused := 0
	for p, ks := range b.keys {
		if ks.Secret == nil && ks.Protection != config.ProtectOperator {
			continue
		}
		err := imp.Stage(new(storage.Batch), p, json.RawMessage(`"x"`), "cutover:m2")
		if err == nil {
			t.Errorf("the importer staged %s, a secret or operator key", p)
		}
		refused++
	}
	if refused < 3 {
		t.Fatalf("only %d secret or operator keys exist; want abs.jwt_secret, acoustid.api_key and cfguests.policy_api_token at least", refused)
	}
}

// booksPinned is the exact class of every key whose protection is not the
// default Admin of the rename table's admin column: the owner trust root and
// the two operator-only secrets (05-config 1.3.7). A key added to either
// group is a reviewed line here.
var booksPinned = map[config.Path]config.Protection{
	"modules.books.identity.owner_email":           config.ProtectOwner,
	"modules.books.identity.cf_access_team_domain": config.ProtectOwner,
	"modules.books.identity.cf_access_aud":         config.ProtectOwner,
	"modules.books.cfaccess.allowed_emails":        config.ProtectOwner,
	"modules.books.abs.jwt_secret":                 config.ProtectOperator,
	"modules.books.cfguests.policy_api_token":      config.ProtectOperator,
}

// TestBooksSettings_ProtectionMatchesTable: the books half of
// TestRenameTable_RefuseVerdictsMatchConfig. A retired name whose canonical
// path is a books key refuses when config itself would refuse it (a key at
// ProtectAdmin or above, or a secret, anywhere under the path) and warns
// otherwise; the table's admin column says the same of every row; and the
// pinned keys read exactly their class.
func TestBooksSettings_ProtectionMatchesTable(t *testing.T) {
	b := newBooksStore(t)
	refuses := func(p config.Path) (known, refuse bool) {
		below := b.under(p)
		for _, ks := range below {
			if ks.Protection >= config.ProtectAdmin || ks.Secret != nil {
				refuse = true
			}
		}
		return len(below) > 0, refuse
	}
	compared, adminRows := 0, 0
	for _, r := range cutover.Table().Rows {
		for _, re := range r.Retired {
			if !strings.HasPrefix(string(re.Canonical), booksPrefix) || strings.Contains(string(re.Canonical), "<") {
				continue
			}
			known, want := refuses(re.Canonical)
			if !known {
				continue // TestRenameTable_EveryKeyRegistered_Books reports it
			}
			compared++
			if got := re.Verdict == cutover.VerdictRefuse; got != want {
				t.Errorf("retired name %s of %s: the table's verdict is %s, config refuses=%v", re.Name, re.Canonical, re.Verdict, want)
			}
		}
	}
	for _, bt := range booksTargets(t) {
		if bt.whole || strings.Contains(string(bt.path), "<") || bt.class == cutover.ClassSecret {
			continue
		}
		ks, ok := b.keys[bt.path]
		if !ok {
			continue
		}
		adminRows++
		if got := ks.Protection >= config.ProtectAdmin; got != bt.admin {
			t.Errorf("%s (HEAD %s): the table's admin column is %v, the key's protection is %v", bt.path, bt.head, bt.admin, ks.Protection)
		}
	}
	for p, want := range booksPinned {
		ks, ok := b.keys[p]
		switch {
		case !ok:
			t.Errorf("pinned key %s is not declared", p)
		case ks.Protection != want:
			t.Errorf("%s protection = %v, want %v", p, ks.Protection, want)
		}
	}
	// No other key is Owner or Operator: the pinned list is the whole of it.
	for p, ks := range b.keys {
		if _, ok := booksPinned[p]; !ok && ks.Protection >= config.ProtectOwner {
			t.Errorf("%s is %v and not in booksPinned", p, ks.Protection)
		}
	}
	if compared == 0 || adminRows == 0 {
		t.Fatalf("a comparison ran on nothing: verdicts=%d rows=%d", compared, adminRows)
	}
	t.Logf("%d retired-name verdicts and %d admin columns compared", compared, adminRows)
}

// shapeRules are 05-config 1.3.7's name shapes that need ProtectAdmin or a
// waiver with a reason: a URL, a path, an executable, a network trust list,
// key material, a signing key or credential token.
var shapeRules = []struct {
	what     string
	contains []string
	suffix   []string
}{
	{"URL", []string{"endpoint"}, []string{"_url"}},
	{"path", nil, []string{"_dir", "_path", "_file"}},
	{"executable", nil, []string{"_bin", "_command", "_exe"}},
	{"network trust list", []string{"proxies", "origins", "allowlist", "client_ip"}, nil},
	{"key material", []string{"recipient", "encryption", "keyring"}, []string{"_key", "_key_file"}},
	{"signing key or credential token", []string{"jwt", "signing"}, []string{"_secret", "_token"}},
}

// TestBooksSettings_ShapeRuleClassified is the books half of 1.3.7's
// TestEveryURLPathOrSignInKeyClassified (the core side does not exist yet):
// a books key whose name looks like a URL, a path, an executable, a trust
// list, key material or a credential has ProtectAdmin or above, or a
// ProtectionWaivers entry with a reason. The waiver set is pinned, so adding
// or dropping one is visible. The waived keys are HEAD's unclassed ones whose
// table admin column is false (see seams.d/F3-07.md, surfaced to the owner).
func TestBooksSettings_ShapeRuleClassified(t *testing.T) {
	b := newBooksStore(t)
	waived := map[config.Path]bool{}
	for p, reason := range booksProtectionWaivers {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("waiver of %s has no reason", p)
		}
		waived[p] = true
	}
	want := []string{
		"modules.books.cfguests.guest_service_tokens",
		"modules.books.cfguests.invite_email_service_token",
		"modules.books.embedding.base_url",
		"modules.books.whisper.endpoints",
		"modules.books.whisper.remote_url",
	}
	var gotWaived []string
	for p := range waived {
		gotWaived = append(gotWaived, string(p))
	}
	sort.Strings(gotWaived)
	if !slices.Equal(gotWaived, want) {
		t.Errorf("waived keys = %v, want %v", gotWaived, want)
	}
	for p, ks := range b.keys {
		leaf := string(p)
		if i := strings.LastIndex(leaf, "."); i >= 0 {
			leaf = leaf[i+1:]
		}
		for _, rule := range shapeRules {
			match := false
			for _, c := range rule.contains {
				match = match || strings.Contains(leaf, c)
			}
			for _, s := range rule.suffix {
				match = match || strings.HasSuffix(leaf, s)
			}
			// 1.3.7 words the key-material rule as "a non-secret field named
			// *_key": a secrets.Ref key is guarded as a secret (never stored,
			// retired names refuse boot), so acoustid.api_key is not a miss.
			if rule.what == "key material" && ks.Secret != nil {
				continue
			}
			if match && ks.Protection < config.ProtectAdmin && !waived[p] {
				t.Errorf("%s looks like a %s but is %v and has no waiver", p, rule.what, ks.Protection)
			}
		}
	}
}

// TestBooksSettings_ReloadClasses pins the two keys HEAD itself reads once at
// start; every other books key is live until its consumer moves onto the
// facade (seams.d/F3-07.md). A key staged by M2 cannot be ReloadBoot, so the
// strictest class is restart.
func TestBooksSettings_ReloadClasses(t *testing.T) {
	b := newBooksStore(t)
	want := map[config.Path]config.Reload{
		"modules.books.search.enabled_sort_indexes": config.ReloadRestart,
		"modules.books.dedup.chromem_lazy":          config.ReloadRestart,
	}
	for p, ks := range b.keys {
		w, ok := want[p]
		if !ok {
			w = config.ReloadLive
		}
		if ks.Reload != w {
			t.Errorf("%s reload = %v, want %v", p, ks.Reload, w)
		}
	}
}

// TestBooksSettings_ScheduleKeysAreScheduleConfig: every field of
// schedulerDefaults().Schedules (config.go) is a tasks.ScheduleConfig key
// under the R2-7 form, and the store declares exactly those keys under the
// prefix, so the scheduler's declaration check accepts them (the kernel half is
// core/kernel's TestScheduleKeys_ModuleKeysDeclared). The set is derived from
// the struct, not counted, so a schedule key added there is checked here.
func TestBooksSettings_ScheduleKeysAreScheduleConfig(t *testing.T) {
	const prefix = "modules.books.scheduler.schedules."
	b := newBooksStore(t)
	want := map[string]bool{}
	st := reflect.TypeOf(schedulerDefaults().Schedules)
	for i := range st.NumField() {
		f := st.Field(i)
		if f.Type != reflect.TypeFor[tasks.ScheduleConfig]() {
			t.Errorf("Schedules.%s is %s, not tasks.ScheduleConfig", f.Name, f.Type)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		want[prefix+name] = true
	}
	if len(want) == 0 {
		t.Fatal("schedulerDefaults().Schedules has no fields")
	}
	got := map[string]bool{}
	for p := range b.keys {
		if !strings.HasPrefix(string(p), prefix) {
			continue
		}
		got[string(p)] = true
		if !want[string(p)] {
			t.Errorf("%s is declared but is no field of schedulerDefaults().Schedules", p)
		}
		raw := b.keys[p].Effective
		var sc map[string]any
		if err := json.Unmarshal(raw, &sc); err != nil {
			t.Errorf("%s: %v", p, err)
		} else if _, ok := sc["enabled"]; !ok {
			t.Errorf("%s: %s is not a ScheduleConfig", p, raw)
		}
	}
	for p := range want {
		if !got[p] {
			t.Errorf("schedulerDefaults().Schedules has %s, which the store does not declare", p)
		}
	}
}

// TestHeadMaintenanceWindowIsCoresNightly: the window name the cutover writes
// into a scheduled entry's placement is core's nightly window.
func TestHeadMaintenanceWindowIsCoresNightly(t *testing.T) {
	if got, want := cutover.HeadMaintenanceWindow, tasks.NightlyWindow().Name; got != want {
		t.Fatalf("cutover.HeadMaintenanceWindow = %q, core's nightly window is %q", got, want)
	}
}
