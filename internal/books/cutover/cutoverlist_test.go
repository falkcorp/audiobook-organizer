// file: internal/books/cutover/cutoverlist_test.go
// version: 1.0.1
// guid: 0c2d5a52-4b8e-45a8-9e3d-8a2d6a5e9d02
// last-edited: 2026-11-11

package cutover

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

const (
	booksLevelVersion = 69 // books' next contiguous version: LegacyCap (66) + 3
	secondLevel       = 2
)

// dataStep is a cutover data step of owner, raised to level, behind after.
func dataStep(id string, owner storage.Owner, level int, after ...storage.StepRef) datamigrate.DataMigration {
	return datamigrate.DataMigration{ID: id, Owner: owner, Cutover: true, Compat: storage.Breaking, RaisesLevel: level, After: after}
}

// validList is 03 §3.8's order over this package's constructors: books' level
// step, core's, M2, M8.
func validList() CutoverList {
	m2 := ConfigStep(nil, nil, ConfigStepOptions{LevelStepVersion: booksLevelVersion, DatabasePath: rigStore})
	return CutoverList{
		Target:       storage.Levels{BooksOwner: BooksCutoverLevel, "core": 1},
		FirstVersion: storage.Levels{BooksOwner: booksLevelVersion},
		Entries: []ListEntry{
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion, BooksCutoverLevel)),
			LevelEntry("core/storage", storage.CoreLevelStep(1, 1)),
			DataEntry(m2),
			RewriteEntry(RewriteStep()),
		},
	}
}

func problems(t *testing.T, l CutoverList) []string {
	t.Helper()
	err := ValidateCutoverList(l)
	if err == nil {
		return nil
	}
	var ce *CutoverListError
	if !errors.As(err, &ce) || !errors.Is(err, ErrInvalidCutoverList) {
		t.Fatalf("the error %v is not a *CutoverListError matching ErrInvalidCutoverList", err)
	}
	return ce.Problems
}

func wantProblem(t *testing.T, l CutoverList, sub string) {
	t.Helper()
	ps := problems(t, l)
	for _, p := range ps {
		if strings.Contains(p, sub) {
			return
		}
	}
	t.Errorf("problems %q lack one containing %q", ps, sub)
}

// TestCutoverList_LevelsFirstContiguousOneM8Last: over the list built from
// this package's constructors in 03 §3.8's order the check passes, and so does
// a two-flip list; a list with no level step for a raised owner, a data step
// before its flip's level step, a gap in an owner's versions, two M8s, or M8
// before a data step fails.
func TestCutoverList_LevelsFirstContiguousOneM8Last(t *testing.T) {
	t.Run("the list of this row passes", func(t *testing.T) {
		if ps := problems(t, validList()); len(ps) != 0 {
			t.Fatalf("the list built from this row's constructors has problems: %q", ps)
		}
	})

	t.Run("a two-flip list passes", func(t *testing.T) {
		l := validList()
		second := dataStep("books.second-flip", BooksOwner, secondLevel, BooksLevelStepRef(booksLevelVersion+1))
		l.Target[BooksOwner] = secondLevel
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+1, secondLevel, BooksLevelStepRef(booksLevelVersion))),
			DataEntry(second),
			l.Entries[3],
		}
		if ps := problems(t, l); len(ps) != 0 {
			t.Fatalf("a second flip at the next version, ordered before its data step, has problems: %q", ps)
		}
	})

	t.Run("no level step for an owner the build raises", func(t *testing.T) {
		l := validList()
		l.Entries = append(l.Entries[:1:1], l.Entries[2:]...) // no core level step
		wantProblem(t, l, "core is raised to 1 by this build but the list has no level step for it")
	})

	t.Run("no level step at all for books", func(t *testing.T) {
		l := validList()
		l.Entries = l.Entries[1:]
		wantProblem(t, l, "books is raised to 1 by this build but the list has no level step for it")
		wantProblem(t, l, "the level step books.level-1 is not ordered before it")
	})

	t.Run("a data step ordered before its level step", func(t *testing.T) {
		l := validList()
		l.Entries = []ListEntry{l.Entries[2], l.Entries[0], l.Entries[1], l.Entries[3]}
		wantProblem(t, l, "is not ordered before it")
	})

	t.Run("a data step that does not name the level step in its After", func(t *testing.T) {
		l := validList()
		l.Entries[2] = DataEntry(ConfigStep(nil, nil, ConfigStepOptions{LevelStepVersion: booksLevelVersion + 5, DatabasePath: rigStore}))
		wantProblem(t, l, "does not name books.level-1")
	})

	t.Run("a gap in an owner's versions", func(t *testing.T) {
		l := validList()
		l.Target[BooksOwner] = secondLevel
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+2, secondLevel)),
			DataEntry(dataStep("books.second-flip", BooksOwner, secondLevel, BooksLevelStepRef(booksLevelVersion+2))),
			l.Entries[3],
		}
		wantProblem(t, l, "are not contiguous: version 71 follows version 69")
	})

	t.Run("books' first level step is not its next contiguous version", func(t *testing.T) {
		l := validList()
		l.FirstVersion[BooksOwner] = booksLevelVersion - 1
		wantProblem(t, l, "first level step is version 69, want its next contiguous version 68")
	})

	t.Run("two M8s", func(t *testing.T) {
		l := validList()
		l.Entries = append(l.Entries, RewriteEntry(RewriteStep()))
		wantProblem(t, l, "2 M8 steps are registered")
		wantProblem(t, l, "M8 is entry 3 of 5")
	})

	t.Run("no M8", func(t *testing.T) {
		l := validList()
		l.Entries = l.Entries[:3]
		wantProblem(t, l, "no M8")
	})

	t.Run("M8 before a data step", func(t *testing.T) {
		l := validList()
		l.Entries = []ListEntry{l.Entries[0], l.Entries[1], l.Entries[3], l.Entries[2]}
		wantProblem(t, l, "must be the last step")
	})

	t.Run("M8 that is not books.rewrite", func(t *testing.T) {
		l := validList()
		l.Entries[3] = RewriteEntry(storage.NewRewriteStep(BooksOwner, "books.rewrite-2"))
		wantProblem(t, l, "the one fixed ID, D15")
	})

	t.Run("a data step that is not a cutover step", func(t *testing.T) {
		l := validList()
		bad := dataStep("books.not-cutover", BooksOwner, BooksCutoverLevel, BooksLevelStepRef(booksLevelVersion))
		bad.Cutover = false
		l.Entries[2] = DataEntry(bad)
		wantProblem(t, l, "is not a cutover step")
	})

	t.Run("level steps that do not rise", func(t *testing.T) {
		l := validList()
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+1, BooksCutoverLevel)),
			l.Entries[3],
		}
		wantProblem(t, l, "do not rise")
	})

	t.Run("a level step run that guards no data step", func(t *testing.T) {
		l := validList()
		l.Target[BooksOwner] = secondLevel
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+1, secondLevel, BooksLevelStepRef(booksLevelVersion))),
			l.Entries[3],
		}
		wantProblem(t, l, "guard no data step before M8")
	})

	t.Run("a level step that is not named for its level", func(t *testing.T) {
		l := validList()
		m := BooksLevelStep(booksLevelVersion, BooksCutoverLevel)
		m.Name = "books.flip"
		l.Entries[0] = LevelEntry(BooksOwner, m)
		wantProblem(t, l, "is not a level step")
	})
}

// flipOpts are FlipOptions over this file's pinned level version, with M2's
// minimum inputs; identity adds the identity flip over the identity fixtures'
// M3 config (the rig's lazy importers, never called here).
func flipOpts(t testing.TB, identity bool) FlipOptions {
	t.Helper()
	opts := FlipOptions{
		LevelStepVersion: booksLevelVersion,
		Config:           ConfigStepOptions{DatabasePath: rigStore},
	}
	if identity {
		h := &idHandles{}
		opts.Identity = &IdentityFlipOptions{Config: m3.Config{
			Atoms: idAtoms(), SeedRoles: idSeedRoles(), Catalog: idCatalog(t), Ambient: idAmbient(),
			Auth: lazyAuth{h}, Roles: lazyRoles{h}, Native: lazyNative{h}, Audit: lazyAudit{h},
			Legacy: func() storage.Reader { _, _, _, l := h.get(); return l },
			Now:    func() time.Time { return idNow }, KeyTTL: idKeyTTL,
			Logger: slog.New(slog.DiscardHandler),
		}}
	}
	return opts
}

func mustFlip(t testing.TB, identity bool) Flip {
	t.Helper()
	f, err := NewFlip(nil, nil, flipOpts(t, identity))
	if err != nil {
		t.Fatalf("NewFlip(identity=%v): %v", identity, err)
	}
	return f
}

// flipList is f's registered list with core's level step, against target.
func flipList(f Flip, target int) CutoverList {
	return CutoverList{
		Target:       storage.Levels{BooksOwner: target, "core": 1},
		FirstVersion: storage.Levels{BooksOwner: booksLevelVersion},
		Entries:      f.Entries(LevelEntry("core/storage", storage.CoreLevelStep(1, 1))),
	}
}

func stepRefs(refs ...storage.StepRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.String()
	}
	return out
}

// entryNames is a list's entries as the step each names, in order.
func entryNames(es []ListEntry) []string {
	var out []string
	for _, e := range es {
		switch e.kind {
		case entryLevel:
			out = append(out, e.level.Name)
		case entryData:
			out = append(out, e.data.ID)
		case entryRewrite:
			out = append(out, e.rewrite.ID)
		}
	}
	return out
}

// TestFlip_IdentityOnListIsValid: the real NewFlip with the identity flip on
// registers books.level-1, M2, books.level-2, M3 and M8 in that order, which
// ValidateCutoverList accepts against books level 2; the off list is F3's alone
// and passes against books level 1.
func TestFlip_IdentityOnListIsValid(t *testing.T) {
	on := mustFlip(t, true)
	l := flipList(on, BooksIdentityLevel)
	if err := ValidateCutoverList(l); err != nil {
		t.Fatalf("the identity-on list is invalid: %v", err)
	}
	want := []string{"core.level-1", BooksCutoverLevelStepID, ConfigCutoverID, BooksIdentityLevelStepID, IdentityCutoverID, RewriteID}
	if got := entryNames(l.Entries); !slices.Equal(got, want) {
		t.Errorf("identity-on entries = %q, want %q", got, want)
	}
	if err := ValidateCutoverList(flipList(on, BooksCutoverLevel)); err == nil {
		t.Error("the identity-on list passed against books level 1, but its last level step raises to 2")
	}

	off := mustFlip(t, false)
	l = flipList(off, BooksCutoverLevel)
	if err := ValidateCutoverList(l); err != nil {
		t.Fatalf("the identity-off list is invalid: %v", err)
	}
	want = []string{"core.level-1", BooksCutoverLevelStepID, ConfigCutoverID, RewriteID}
	if got := entryNames(l.Entries); !slices.Equal(got, want) {
		t.Errorf("identity-off entries = %q, want %q", got, want)
	}
	if err := ValidateCutoverList(flipList(off, BooksIdentityLevel)); err == nil {
		t.Error("the identity-off list passed against books level 2")
	}
}

// TestFlip_IdentityOffIsTodaysList: with no Identity option the flip is exactly
// F3's: one level step at LevelStepVersion, M2 alone, M8 After M2, Spec level 1
// with no identity claim retired and no bootstrap key claim added.
func TestFlip_IdentityOffIsTodaysList(t *testing.T) {
	f := mustFlip(t, false)
	if len(f.Levels) != 1 {
		t.Fatalf("%d level steps, want 1", len(f.Levels))
	}
	if l := f.Levels[0]; l.Name != BooksCutoverLevelStepID || l.Version != booksLevelVersion || l.RaisesLevel != BooksCutoverLevel || len(l.After) != 0 {
		t.Errorf("level step %s v%d raises %d after %v", l.Name, l.Version, l.RaisesLevel, l.After)
	}
	if len(f.Data) != 1 || f.Data[0].ID != ConfigCutoverID {
		t.Fatalf("data steps %v, want M2 alone", dataIDs(f.Data))
	}
	if want := []string{storage.StepRef{Owner: BooksOwner, DataMigration: ConfigCutoverID}.String()}; !slices.Equal(stepRefs(f.Rewrite.After...), want) {
		t.Errorf("M8 After = %v, want %v", stepRefs(f.Rewrite.After...), want)
	}

	base := store.BooksSpec(nil)
	spec := f.Spec(base)
	if spec.Level != BooksCutoverLevel {
		t.Errorf("Spec.Level = %d, want %d", spec.Level, BooksCutoverLevel)
	}
	if got := len(spec.Migrations) - len(base.Migrations); got != 1 || spec.Migrations[len(spec.Migrations)-1].Name != BooksCutoverLevelStepID {
		t.Errorf("Spec appended %d steps, want books.level-1 alone", got)
	}
	retired := m3.SourcePrefixes()
	for _, c := range spec.Claims {
		if c.Prefix != "" && slices.Contains(retired, c.Prefix) && c.Mode == storage.ClaimRetired {
			t.Errorf("identity claim %q retired with the identity flip off", c.Prefix)
		}
	}
	if len(spec.Claims) != len(base.Claims) {
		t.Errorf("Spec has %d claims, base %d: the flip off adds none", len(spec.Claims), len(base.Claims))
	}
	// The pure shape is the same list without the data steps.
	shape := NewFlipShape(flipOpts(t, false))
	if len(shape.Data) != 0 || len(shape.Levels) != 1 || !slices.Equal(stepRefs(shape.Rewrite.After...), stepRefs(f.Rewrite.After...)) {
		t.Errorf("shape: %d levels, %d data, M8 After %v", len(shape.Levels), len(shape.Data), stepRefs(shape.Rewrite.After...))
	}
}

func dataIDs(ds []datamigrate.DataMigration) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

// TestFlip_IdentityOnSpecAndSteps: with the identity flip on Spec declares
// level 2 with books.level-1 and books.level-2 at contiguous versions, retires
// the identity families' claims and adds the bootstrap rows' retired exact-key
// claims; M3 runs After books.level-2 and M2, raises books to level 2, and M8
// runs After both; the pure shape (what the manifest's spec and rewrite come
// from) carries the same levels, claims and M8.
func TestFlip_IdentityOnSpecAndSteps(t *testing.T) {
	f := mustFlip(t, true)
	base := store.BooksSpec(nil)
	baseClaims := slices.Clone(base.Claims)
	spec := f.Spec(base)
	if !reflect.DeepEqual(base.Claims, baseClaims) {
		t.Error("Spec mutated the base claims")
	}
	if spec.Level != BooksIdentityLevel {
		t.Errorf("Spec.Level = %d, want %d", spec.Level, BooksIdentityLevel)
	}
	tail := spec.Migrations[len(base.Migrations):]
	if len(tail) != 2 {
		t.Fatalf("Spec appended %d steps, want 2", len(tail))
	}
	for i, want := range []struct {
		name  string
		level int
	}{{BooksCutoverLevelStepID, BooksCutoverLevel}, {BooksIdentityLevelStepID, BooksIdentityLevel}} {
		if m := tail[i]; m.Name != want.name || m.Version != booksLevelVersion+i || m.RaisesLevel != want.level {
			t.Errorf("step %d is %s v%d raising %d, want %s v%d raising %d", i, m.Name, m.Version, m.RaisesLevel, want.name, booksLevelVersion+i, want.level)
		}
	}
	if want := stepRefs(BooksLevelStepRef(booksLevelVersion)); !slices.Equal(stepRefs(tail[1].After...), want) {
		t.Errorf("books.level-2 After = %v, want %v", stepRefs(tail[1].After...), want)
	}

	retired := m3.SourcePrefixes()
	n := 0
	for _, c := range spec.Claims {
		if c.Prefix != "" && slices.Contains(retired, c.Prefix) {
			n++
			if c.Mode != storage.ClaimRetired {
				t.Errorf("identity claim %q is %v, want retired", c.Prefix, c.Mode)
			}
		}
	}
	if n == 0 {
		t.Error("no identity claim found in the spec: the check is vacuous")
	}
	for _, k := range m3.SourceKeys() {
		i := slices.IndexFunc(spec.Claims, func(c storage.Claim) bool { return c.Key == k })
		if i < 0 {
			t.Errorf("no claim for the bootstrap row %q", k)
			continue
		}
		if c := spec.Claims[i]; c.Mode != storage.ClaimRetired || !c.Serialized {
			t.Errorf("bootstrap row claim %q: mode %v serialized %v, want retired and serialized", k, c.Mode, c.Serialized)
		}
	}
	for _, name := range headSecretRows() {
		i := slices.IndexFunc(spec.Claims, func(c storage.Claim) bool { return c.Key == string(settingKey(name)) })
		if i < 0 || spec.Claims[i].Mode != storage.ClaimRetired {
			t.Errorf("M2's legacy secret row %q is not retired", name)
		}
	}

	// M3's declaration.
	if len(f.Data) != 2 || f.Data[0].ID != ConfigCutoverID || f.Data[1].ID != IdentityCutoverID {
		t.Fatalf("data steps %v, want M2 then M3", dataIDs(f.Data))
	}
	m2, m3step := f.Data[0], f.Data[1]
	if want := stepRefs(BooksLevelStepRef(booksLevelVersion)); !slices.Equal(stepRefs(m2.After...), want) {
		t.Errorf("M2 After = %v, want %v: the identity flip leaves M2 as it was", stepRefs(m2.After...), want)
	}
	wantAfter := stepRefs(BooksLevelStepRef(booksLevelVersion+1), storage.StepRef{Owner: BooksOwner, DataMigration: ConfigCutoverID})
	if !slices.Equal(stepRefs(m3step.After...), wantAfter) {
		t.Errorf("M3 After = %v, want %v (books.level-2 and M2)", stepRefs(m3step.After...), wantAfter)
	}
	if m3step.RaisesLevel != BooksIdentityLevel || m2.RaisesLevel != BooksCutoverLevel {
		t.Errorf("M2 raises %d, M3 raises %d", m2.RaisesLevel, m3step.RaisesLevel)
	}
	wantRewrite := stepRefs(storage.StepRef{Owner: BooksOwner, DataMigration: ConfigCutoverID}, storage.StepRef{Owner: BooksOwner, DataMigration: IdentityCutoverID})
	if !slices.Equal(stepRefs(f.Rewrite.After...), wantRewrite) {
		t.Errorf("M8 After = %v, want %v", stepRefs(f.Rewrite.After...), wantRewrite)
	}

	// The shape the manifest builds with no importers is the same declaration.
	shape := NewFlipShape(flipOpts(t, true))
	if len(shape.Data) != 0 {
		t.Errorf("the shape carries %d data steps", len(shape.Data))
	}
	ss := shape.Spec(store.BooksSpec(nil))
	if ss.Level != spec.Level || len(ss.Migrations) != len(spec.Migrations) || !reflect.DeepEqual(ss.Claims, spec.Claims) {
		t.Errorf("the shape's spec differs from the flip's: level %d/%d, %d/%d steps, claims equal %v", ss.Level, spec.Level,
			len(ss.Migrations), len(spec.Migrations), reflect.DeepEqual(ss.Claims, spec.Claims))
	}
	if !slices.Equal(stepRefs(shape.Rewrite.After...), stepRefs(f.Rewrite.After...)) {
		t.Errorf("the shape's M8 After %v differs from the flip's %v", stepRefs(shape.Rewrite.After...), stepRefs(f.Rewrite.After...))
	}
}

// TestFlip_IdentityRefusesAnIncompleteConfig: NewFlip's one error is M3's, and
// it carries no partial list.
func TestFlip_IdentityRefusesAnIncompleteConfig(t *testing.T) {
	opts := flipOpts(t, true)
	opts.Identity.Config.Atoms = nil
	f, err := NewFlip(nil, nil, opts)
	if err == nil {
		t.Fatal("NewFlip accepted an identity flip with no atom table")
	}
	if len(f.Levels) != 0 || len(f.Data) != 0 {
		t.Errorf("a refused flip returned %d levels and %d data steps", len(f.Levels), len(f.Data))
	}
}

// TestFlip_IdentityOnRegistersWithStorage: books' spec through Flip.Spec with
// the identity flip on, and M2 and M3 as data steps, open through storage's
// registrar (SpecError, cycle and unknown-step checks over the DAG): books.level-2
// is not After M2 (the owner's data steps run after its highest versioned step,
// so that edge would be a cycle), M3 names M2 and books.level-2, and M8 is
// accepted with both in its After. The off list registers too.
func TestFlip_IdentityOnRegistersWithStorage(t *testing.T) {
	for _, identity := range []bool{false, true} {
		f := mustFlip(t, identity)
		owners := []storage.OwnerSpec{{Owner: BooksOwner, Spec: f.Spec(store.BooksSpec(nil)), Enabled: true}}
		for _, o := range []storage.Owner{"core/auth", "core/rbac", "core/audit", "core/secrets", "core/config"} {
			owners = append(owners, storage.OwnerSpec{Owner: o, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true})
		}
		dms, err := kernel.NewDataMigrations(kernel.DataMigrationsConfig{ChunkItems: 1, Build: rigBuildNew, Logger: slog.New(slog.DiscardHandler)}, owners, f.Data...)
		if err != nil {
			t.Fatalf("identity=%v: NewDataMigrations: %v", identity, err)
		}
		mem := vfs.NewMem()
		if err := mem.MkdirAll(rigDir, 0o700); err != nil {
			t.Fatal(err)
		}
		o, err := storage.Open(context.Background(), storage.OpenOptions{Path: rigStore, FS: storage.FSOf(mem), Owners: owners, Build: rigBuildNew,
			FreeSpaceMargin: 1, DataSteps: dms.Steps(), Clock: crashmatrix.Now})
		if err != nil {
			t.Fatalf("identity=%v: the flip does not register with storage: %v", identity, err)
		}
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
