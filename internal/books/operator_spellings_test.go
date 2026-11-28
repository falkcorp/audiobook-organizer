// file: internal/books/operator_spellings_test.go
// version: 1.0.0
// guid: 3c9d7e41-8a25-4f06-b1d3-6e0a5c2f9b87
// last-edited: 2026-11-28

package books

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
)

// The class behind F1 of round 1 of #5080. The retired-setting refusal matched the
// exact canonical spelling of a refused setting, but core's env reader accepts
// more than one spelling of every name, and a spelling the refusal did not know
// was read by nothing: the value was dropped without a word. This table walks
// EVERY spelling core accepts, for EVERY row of the rename table and EVERY
// refused family, against the real code (the books refusal, then core's
// OperatorConfig over the books options), and requires each to land somewhere:
//
//	REFUSED  the boot or config check stops, naming the name
//	MAPPED   core placed it on a key (KeyState.LockedBy), on a passthrough path
//	         (OperatorValue.By), or refused its value naming the variable
//	WARNED   core's retired-name warning named it (a warn row: ignored loudly)
//	IGNORED  none of those: the value is dropped, which is the bug
//
// The spellings core accepts (mediacore/config/env.go):
//
//	S1 the canonical ABO_<PATH> name, capitals only: envName in readWanted
//	   (env.go:301-333), exactUpperName (env.go:206-219)
//	S2 <NAME>_FILE of any name that places, the value read from the file it
//	   names: resolve (env.go:224-238), readWanted (env.go:316-319), value
//	S3 an env binding, OPENAI_API_KEY and OPENAI_BASE_URL (ConfigEnvBindings),
//	   and its _FILE form: readWanted (env.go:308-314)
//	S4 an INTEGRATIONS_* name with each "_" read as ".", "-" or "_" (a path core
//	   cannot list, only test): passthrough (env.go:171-204)
//	S5 a retired name, the exact HEAD spelling only: retiredIsSet reads
//	   envVars[name] (retired.go), so a legacy name has no _FILE form. HEAD's own
//	   reader (viper) never read <NAME>_FILE either, so there is nothing to keep.
//	   (The empty value and the literal null are unset for S1, S3 and S4:
//	   envUnset, env.go:369; for a _FILE the literal is tested against the file's
//	   contents, not the variable.)
//
// Mutants: dropping the _FILE trim in cutover.EnvForms makes the refused rows'
// _FILE spellings IGNORED and fails this table, naming each; the rest of the
// mutant table is in the PR body.

type spellingOutcome string

const (
	outRefused spellingOutcome = "REFUSED"
	outMapped  spellingOutcome = "MAPPED"
	outWarned  spellingOutcome = "WARNED"
	outIgnored spellingOutcome = "IGNORED"
)

// logCapture records core's log messages so a retired-name warning can be seen.
type logCapture struct {
	mu   sync.Mutex
	recs []map[string]string
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *logCapture) WithGroup(string) slog.Handler            { return c }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	rec := map[string]string{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool { rec[a.Key] = a.Value.String(); return true })
	c.mu.Lock()
	c.recs = append(c.recs, rec)
	c.mu.Unlock()
	return nil
}

// observeSpellings sets every variable of names at once (to the sentinel, or to
// a file that holds it for a _FILE name) and says where each value went. One
// config load serves the whole group; a load that fails on one variable names
// it (core reads the value and rejects it: MAPPED, or the retired-name refusal:
// REFUSED) and the group is run again without it. An error that names none of
// the variables is run one name at a time, so a name it does not attribute to
// is IGNORED, never assumed.
func observeSpellings(t *testing.T, names []string, viaFile bool) map[string]spellingOutcome {
	t.Helper()
	const sentinel = "sentinel-gotham-9f21"
	out := map[string]spellingOutcome{}
	valueOf := func(name string) string {
		if !viaFile {
			return sentinel
		}
		f := filepath.Join(t.TempDir(), "value")
		if err := os.WriteFile(f, []byte(sentinel+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	values := map[string]string{}
	for _, n := range names {
		values[n] = valueOf(n)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.SecureStateDirEnv, filepath.Join(t.TempDir(), "state"))
	pending := slices.Clone(names)
	setEnv := func(set []string) {
		for _, n := range names {
			_ = os.Unsetenv(n)
		}
		for _, n := range set {
			t.Setenv(n, values[n])
		}
	}
	resolve := func(n string, o spellingOutcome) {
		out[n] = o
		pending = slices.DeleteFunc(pending, func(x string) bool { return x == n })
	}
	for len(pending) > 0 {
		setEnv(pending)
		environ := make([]string, 0, len(pending))
		for _, n := range pending {
			environ = append(environ, n+"="+values[n])
		}
		if err := RefuseRetiredOperatorSettings("", environ); err != nil {
			if strings.Contains(err.Error(), sentinel) {
				t.Errorf("the refusal prints a value: %v", err)
			}
			found := false
			for _, n := range slices.Clone(pending) {
				if strings.Contains(err.Error(), "the environment variable "+n+" (") {
					resolve(n, outRefused)
					found = true
				}
			}
			if !found {
				t.Fatalf("the refusal names none of the variables set: %v", err)
			}
			continue
		}
		logs := &logCapture{}
		opts := bootOptions(BootInputs{})
		opts.CoreDefaults = coreDefaults
		opts.Logger = slog.New(logs)
		acc, err := app.OperatorConfig(t.Context(), opts, []module.Module{Module()})
		if err != nil {
			var rn *coreconfig.RetiredNameError
			attributed := false
			if errors.As(err, &rn) {
				for _, u := range rn.Uses {
					if slices.Contains(pending, u.Name) {
						resolve(u.Name, outRefused)
						attributed = true
					}
				}
			}
			for _, n := range slices.Clone(pending) {
				if !attributed && strings.Contains(err.Error(), n) && !strings.Contains(err.Error(), n+"_FILE") {
					// core read it and rejected the value, naming the variable
					resolve(n, outMapped)
					attributed = true
					break
				}
			}
			if attributed {
				continue
			}
			if len(pending) == 1 {
				resolve(pending[0], outIgnored)
				continue
			}
			for _, n := range slices.Clone(pending) {
				for k, o := range observeSpellings(t, []string{n}, viaFile) {
					resolve(k, o)
				}
			}
			continue
		}
		for _, ks := range acc.Keys() {
			if n, ok := strings.CutPrefix(ks.LockedBy, "env "); ok && slices.Contains(pending, n) {
				out[n] = outMapped
			}
		}
		for _, v := range acc.PassthroughValues() {
			if n, ok := strings.CutPrefix(v.By, "env "); ok && slices.Contains(pending, n) {
				out[n] = outMapped
			}
		}
		for _, r := range logs.recs {
			if n := r["name"]; slices.Contains(pending, n) && strings.Contains(r["msg"], "retired config name") {
				if _, ok := out[n]; !ok {
					out[n] = outWarned
				}
			}
		}
		for _, n := range pending {
			if _, ok := out[n]; !ok {
				out[n] = outIgnored
			}
		}
		_ = acc.Close(context.Background())
		pending = nil
	}
	setEnv(nil)
	return out
}

// tableRefusesCoreWarns are the retired names whose table verdict is refuse and
// whose observed behaviour is a warning: the one finding of this table beyond
// round 1's F1. plugins.webhook.settings.secret becomes a secret under
// integrations.notify.webhook-<n>.secret, a family path with no concrete
// canonical form, so the row's Canonical is "" and core's retiredJudge answers
// retiredWarn for an empty Canonical before it reads the app's Refuse statement
// (core/config/retired.go, the first case of retiredJudge). The name is warned,
// loudly, never silent; but the row says refuse and the generated deploy list
// carries refuse for it. Making the two agree changes what a live boot does
// (core's order of cases, or the table's verdict and the list row), so it is
// reported, not made, here: this entry is pinned so the cell fails the day the
// two agree, and the entry must then be deleted.
var tableRefusesCoreWarns = map[string]string{
	"PLUGINS.WEBHOOK.SETTINGS.SECRET": "table verdict refuse, core warns (Canonical empty): not changed in round 1 of #5080",
}

type spellingCase struct {
	row      string // the rename-table row, or the refused family
	name     string // the env variable
	viaFile  bool
	spelling string // S1..S5
	want     []spellingOutcome
}

// spellingCases generates every accepted spelling of every row and family.
func spellingCases(t *testing.T) (cases []spellingCase, covered map[string]bool, skipped map[string]string) {
	t.Helper()
	covered, skipped = map[string]bool{}, map[string]string{}
	opts := bootOptions(BootInputs{})
	opts.CoreDefaults = coreDefaults
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.SecureStateDirEnv, filepath.Join(t.TempDir(), "state"))
	acc, err := app.OperatorConfig(t.Context(), opts, []module.Module{Module()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acc.Close(context.Background()) }()
	add := func(row, name string, viaFile bool, spelling string, want ...spellingOutcome) {
		if viaFile {
			name += "_FILE"
		}
		cases = append(cases, spellingCase{row, name, viaFile, spelling, want})
		covered[row] = true
	}
	refusedCanon := map[string]bool{}
	for _, r := range cutover.Table().Rows {
		// S5: the exact retired names. A refuse verdict stops the boot, a warn
		// verdict is ignored with core's warning; kept is a binding (S3 below).
		for _, e := range r.Retired {
			if e.File || e.Verdict == cutover.VerdictKept {
				continue
			}
			want := outWarned
			if e.Verdict == cutover.VerdictRefuse {
				want = outRefused
				if _, ok := tableRefusesCoreWarns[e.Name]; ok {
					want = outWarned
				}
			}
			add(r.Head, e.Name, false, "S5", want)
		}
		// S1, S2, S4: each target's canonical name and its _FILE form. A key core
		// declares is MAPPED by its lock; a passthrough path (an integrations path
		// the rename table sends a retired name to) by its operator value.
		for _, tg := range r.Targets {
			p := tg.ConfigPath()
			if p == "" {
				continue
			}
			_, isKey := acc.Lookup(p)
			isPT := cutover.ConfigPassthrough(p)
			if !isKey && !isPT {
				skipped[r.Head+" -> "+string(p)] = "a container or list: core reads its members, not it"
				continue
			}
			spelling := "S1"
			if !isKey {
				spelling = "S4"
			}
			canon := cutover.CanonicalEnv(p)
			refusedCanon[canon] = true
			add(r.Head, canon, false, spelling, outMapped)
			add(r.Head, canon, true, "S2", outMapped)
		}
	}
	// S3: the env bindings.
	for _, b := range cutover.ConfigEnvBindings() {
		add("binding "+b.Name, b.Name, false, "S3", outMapped)
		add("binding "+b.Name, b.Name, true, "S3", outMapped)
	}
	// The refused families (cutover.RefusedOperatorPaths): every spelling is
	// REFUSED, never read by anything. A placeholder is instantiated with a source
	// id of one word and one with a hyphen (core reads "_" as "-" too: the S4
	// walk) and keys of the shapes a host uses; the canonical name of a hyphenated
	// id is the one spelling an env variable can have (capitals and "_").
	for _, rp := range cutover.RefusedOperatorPaths() {
		row := "refused " + rp.Path
		var paths []string
		if strings.Contains(rp.Path, "<") {
			for _, id := range []string{"hardcover", "google-books", "x"} {
				for _, k := range []string{"api_key", "api_token", "client-secret", "k"} {
					paths = append(paths, strings.NewReplacer("<source id>", id, "<k>", k).Replace(rp.Path))
				}
			}
		} else {
			paths = []string{rp.Path}
		}
		for _, p := range paths {
			canon := cutover.CanonicalEnv(coreconfig.Path(p))
			add(row, canon, false, "S1", outRefused)
			add(row, canon, true, "S2", outRefused)
		}
	}
	return cases, covered, skipped
}

func TestOperatorSpellings_EveryAcceptedSpellingIsMappedOrRefused(t *testing.T) {
	cases, covered, skipped := spellingCases(t)
	if len(cases) == 0 {
		t.Fatal("the table generated no case: the class cell proves nothing")
	}
	// Every row is covered: has at least one spelling, or is named below with the
	// reason it has none. A new row cannot slip in without a case.
	noEnv := map[string]string{
		"ai_endpoints[]":                     "a list of structs (the ai/openai instance is configured by its integrations path)",
		"metadata_sources[].credentials.<k>": "the refused family: its spellings are the 'refused' cases",
		"plugins.<id>":                       "HEAD's plugin settings had no env spelling",
	}
	for _, r := range cutover.Table().Rows {
		if covered[r.Head] {
			continue
		}
		if _, ok := skipped[r.Head]; ok {
			continue
		}
		hasSkipped := false
		for k := range skipped {
			if strings.HasPrefix(k, r.Head+" -> ") {
				hasSkipped = true
			}
		}
		if hasSkipped {
			continue
		}
		if _, ok := noEnv[r.Head]; !ok {
			t.Errorf("rename-table row %q has no env spelling case and is not named as having none", r.Head)
		}
	}
	for head := range noEnv {
		if covered[head] {
			t.Errorf("row %q is listed as having no env spelling but has cases: drop it from the list", head)
		}
	}
	// Every refused family has cases, in the three spelling shapes.
	for _, rp := range cutover.RefusedOperatorPaths() {
		var n, nFile int
		for _, c := range cases {
			if c.row == "refused "+rp.Path {
				n++
				if c.viaFile {
					nFile++
				}
			}
		}
		if n == 0 || nFile == 0 {
			t.Errorf("the refused family %s has %d cases (%d of them _FILE)", rp.Path, n, nFile)
		}
	}
	// One load per group: names that cannot share a load are those two spellings
	// of one path (the canonical name and its _FILE form, or a binding and the
	// canonical name of its path), so each of those spellings is its own group.
	groups := map[string][]string{}
	for _, c := range cases {
		key := c.spelling
		if c.spelling == "S3" || c.spelling == "S5" {
			key += "/" + string(c.want[0])
		}
		if c.viaFile {
			key += "/file"
		}
		groups[key] = append(groups[key], c.name)
	}
	got := map[string]spellingOutcome{}
	for key, names := range groups {
		if strings.HasPrefix(key, "S3") {
			for _, n := range names { // a binding and a canonical name of one path conflict
				for k, o := range observeSpellings(t, []string{n}, strings.HasSuffix(key, "/file")) {
					got[k] = o
				}
			}
			continue
		}
		for k, o := range observeSpellings(t, names, strings.HasSuffix(key, "/file")) {
			got[k] = o
		}
	}
	var bad []string
	counts := map[spellingOutcome]int{}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%s", c.spelling, c.name), func(t *testing.T) {
			g := got[c.name]
			counts[g]++
			if !slices.Contains(c.want, g) {
				bad = append(bad, c.name)
				t.Errorf("row %q: %s (%s) is %s, want %v: a value the host set that nothing reads", c.row, c.name, c.spelling, g, c.want)
			}
		})
	}
	t.Logf("%d spellings: %v; %d targets skipped as containers; %d rows with no env spelling", len(cases), counts, len(skipped), len(noEnv))
	if len(bad) > 0 {
		t.Errorf("%d spellings are dropped: %v", len(bad), bad)
	}
}
