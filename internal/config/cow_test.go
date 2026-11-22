// file: internal/config/cow_test.go
// version: 0.1.0
// guid: 3e9b7c2d-5f14-4a86-b0d1-8c6e2f7a9b45
// last-edited: 2026-11-22

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// restoreConfig puts the config back after a test that changes it. It saves
// the mirror, not Get(): while AppConfig is the transitional mirror an earlier
// test in this package may still have assigned it directly.
func restoreConfig(t *testing.T) {
	t.Helper()
	saved := Snapshot()
	t.Cleanup(func() { ReplaceForTest(saved) })
}

func TestGetIsNeverNil(t *testing.T) {
	if Get() == nil {
		t.Fatal("Get() returned nil; callers rely on it never doing so")
	}
}

// TestMutatePublishesANewSnapshotAndLeavesTheOldOneAlone is the core
// copy-on-write property: a reader holding the previous snapshot sees none of
// the write, not its scalars, not its slices, not its maps.
func TestMutatePublishesANewSnapshotAndLeavesTheOldOneAlone(t *testing.T) {
	restoreConfig(t)
	SetForTest(func(c *Config) {
		c.RootDir = "/tmp/gotham"
		c.SupportedExtensions = []string{".mp3", ".m4b"}
		c.Dedup.Signals.Confidence = map[string]DedupKindConfidence{"isbn": {MinConfidence: 0.9}}
	})
	before := Get()
	beforeCopy := before.Clone()

	Mutate(func(c *Config) {
		c.RootDir = "/tmp/batcave"
		c.SupportedExtensions[0] = ".flac" // in place on the copy
		c.Dedup.Signals.Confidence["isbn"] = DedupKindConfidence{MinConfidence: 0.1}
		c.Dedup.Signals.Confidence["asin"] = DedupKindConfidence{MinConfidence: 0.5}
	})

	after := Get()
	if after == before {
		t.Fatal("Mutate did not publish a new snapshot")
	}
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("the previous snapshot changed under a reader: %+v", before)
	}
	if after.RootDir != "/tmp/batcave" || after.SupportedExtensions[0] != ".flac" || after.Dedup.Signals.Confidence["asin"].MinConfidence != 0.5 {
		t.Fatalf("the write did not reach the new snapshot: %+v", after)
	}
}

// TestMutateDoesNotShareAWholesaleAssignedValue: `*c = cfg` must not leave the
// published snapshot sharing cfg's maps with a caller that keeps changing them.
func TestMutateDoesNotShareAWholesaleAssignedValue(t *testing.T) {
	restoreConfig(t)
	cfg := *Get().Clone()
	cfg.SupportedExtensions = []string{".mp3"}
	Mutate(func(c *Config) { *c = cfg })
	cfg.SupportedExtensions[0] = ".wav"
	if got := Get().SupportedExtensions[0]; got != ".mp3" {
		t.Fatalf("the caller's later write reached the published snapshot: %q", got)
	}
}

func TestPanickingMutatePublishesNothing(t *testing.T) {
	restoreConfig(t)
	SetForTest(func(c *Config) { c.RootDir = "/tmp/wayne-manor" })
	before := Get()
	func() {
		defer func() { _ = recover() }()
		Mutate(func(c *Config) {
			c.RootDir = "/tmp/arkham"
			panic("Joker")
		})
	}()
	if Get() != before || Get().RootDir != "/tmp/wayne-manor" {
		t.Fatalf("a panicking Mutate published a half-made config: %q", Get().RootDir)
	}
	if AppConfig.RootDir != "/tmp/wayne-manor" {
		t.Fatalf("a panicking Mutate left a half-made write in the mirror: %q", AppConfig.RootDir)
	}
}

func TestGenerationMovesAfterThePointer(t *testing.T) {
	restoreConfig(t)
	g := Generation()
	SetForTest(func(c *Config) { c.RootDir = "/tmp/gcpd" })
	if Generation() == g {
		t.Fatal("SetForTest did not move Generation")
	}
	if Get().RootDir != "/tmp/gcpd" {
		t.Fatal("Generation moved but Get does not show the write")
	}
}

// TestConcurrentReadersAndWriters is the race the lane exists to remove: under
// -race, readers that Get() and read slices and maps while writers Mutate
// must be clean. Before the copy-on-write config the same loop over
// AppConfig.SupportedExtensions was a reported race.
func TestConcurrentReadersAndWriters(t *testing.T) {
	restoreConfig(t)
	SetForTest(func(c *Config) {
		c.SupportedExtensions = []string{".mp3"}
		c.Dedup.Signals.Confidence = map[string]DedupKindConfidence{"isbn": {MinConfidence: 0.9}}
	})
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for !stop.Load() {
				c := Get()
				_ = len(c.SupportedExtensions)
				for _, e := range c.SupportedExtensions {
					_ = e
				}
				_ = c.Dedup.Signals.Confidence["isbn"]
				_ = c.RootDir
			}
		})
	}
	for i := range 200 {
		Mutate(func(c *Config) {
			c.SupportedExtensions = append(c.SupportedExtensions, ".m4b")
			c.Dedup.Signals.Confidence["isbn"] = DedupKindConfidence{MinConfidence: float64(i)}
			c.RootDir = filepath.Join("/tmp/gotham", "block")
		})
	}
	stop.Store(true)
	wg.Wait()
	if err := CheckSealed(); err != nil {
		t.Fatal(err)
	}
}

// TestSealCatchesAnInPlaceWriteToAPublishedSnapshot: the runtime backstop for
// what the compiler cannot stop. A write through Get() is reported by
// CheckSealed and would panic the next publish.
func TestSealCatchesAnInPlaceWriteToAPublishedSnapshot(t *testing.T) {
	restoreConfig(t)
	SetForTest(func(c *Config) { c.SupportedExtensions = []string{".mp3", ".m4b"} })
	snap := Get()
	snap.SupportedExtensions[1] = ".wav" // the bug the seal exists to catch
	err := CheckSealed()
	if err == nil || !strings.Contains(err.Error(), "SupportedExtensions[1]") {
		t.Fatalf("CheckSealed did not name the written field: %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the next publish did not panic on the written snapshot")
			}
		}()
		SetForTest(func(*Config) {})
	}()
	// Undo the deliberate damage so restoreConfig's publish passes the check.
	snap.SupportedExtensions[1] = ".m4b"
}

func TestReplaceForTestPublishesACopy(t *testing.T) {
	restoreConfig(t)
	cfg := Config{RootDir: "/tmp/ace-chemicals", SupportedExtensions: []string{".mp3"}}
	ReplaceForTest(cfg)
	cfg.SupportedExtensions[0] = ".ogg"
	if got := Get(); got.RootDir != "/tmp/ace-chemicals" || got.SupportedExtensions[0] != ".mp3" {
		t.Fatalf("ReplaceForTest published something that shares the caller's slice: %+v", got)
	}
}

func TestFixedSource(t *testing.T) {
	c := &Config{RootDir: "/tmp/iceberg-lounge"}
	if Fixed(c)() != c {
		t.Fatal("Fixed did not return its config")
	}
	var s Source = Get
	if s() != Get() {
		t.Fatal("config.Get is not usable as a Source")
	}
}

// TestNormalizedAndValidateDoNotChangeTheirReceiver: Validate used to
// normalise its receiver, which made `config.AppConfig.Validate()` at boot a
// field-by-field write to the live config. Both are pure now.
func TestNormalizedAndValidateDoNotChangeTheirReceiver(t *testing.T) {
	c := &Config{DatabaseType: "pebble"} // every zero-means-default field at zero
	before := c.Clone()
	n := c.Normalized()
	_ = c.Validate()
	if !reflect.DeepEqual(c, before) {
		t.Fatal("Normalized or Validate changed its receiver")
	}
	if n.Embedding.VectorBackend != "hnsw" || n.MinBookSizeBytes != 5*1024*1024 || n.MinRescanAgeHours != 144 ||
		n.ChapterConsolidationThresholdMin != DefaultChapterConsolidationThresholdMin || n.LogRetentionDays == 0 {
		t.Fatalf("Normalized did not apply the defaults: %+v", n)
	}
	if n == c {
		t.Fatal("Normalized returned its receiver")
	}
}

func TestNormalizeAndValidatePublishesTheNormalisedConfigWhole(t *testing.T) {
	restoreConfig(t)
	SetForTest(func(c *Config) {
		c.Embedding.VectorBackend = ""
		c.MinBookSizeBytes = 0
		c.MinRescanAgeHours = 0
	})
	before := Get()
	_ = NormalizeAndValidate() // the error depends on the host's paths; only the publish matters here
	after := Get()
	if after == before {
		t.Fatal("NormalizeAndValidate did not publish a new snapshot")
	}
	if before.Embedding.VectorBackend != "" || before.MinBookSizeBytes != 0 {
		t.Fatal("NormalizeAndValidate wrote into the snapshot readers held")
	}
	if after.Embedding.VectorBackend != "hnsw" || after.MinBookSizeBytes != 5*1024*1024 || after.MinRescanAgeHours != 144 {
		t.Fatalf("the published config is not normalised: %+v", after)
	}
}

// readOnlyMethods are the config methods scripts/config_cow_codemod.py lets a
// caller invoke on a published snapshot (READONLY_METHODS). Each is called
// here on a config whose zero-means-default fields are all zero, the inputs
// that would tempt a method to normalise in place.
var readOnlyMethods = map[string]func(c *Config){
	"EffectiveEmbeddingMode":                  func(c *Config) { _ = c.EffectiveEmbeddingMode() },
	"ResolveAIParseBatch":                     func(c *Config) { _ = c.ResolveAIParseBatch() },
	"ResolveChapterConsolidationThresholdMin": func(c *Config) { _ = c.ResolveChapterConsolidationThresholdMin() },
	"ResolveRepairChapterMaxMin":              func(c *Config) { _ = c.ResolveRepairChapterMaxMin() },
	"ThresholdsForModel":                      func(c *Config) { _, _ = c.Dedup.ThresholdsForModel("batcomputer") },
}

func TestReadOnlyMethodsDoNotMutate(t *testing.T) {
	for name, call := range readOnlyMethods {
		c := &Config{}
		before := c.Clone()
		call(c)
		if !reflect.DeepEqual(c, before) {
			t.Errorf("%s changed its receiver; remove it from READONLY_METHODS in scripts/config_cow_codemod.py", name)
		}
	}
}

// TestReadOnlyMethodsMatchTheCodemod: the codemod rewrites calls to exactly
// the methods this file proves read-only, no more.
func TestReadOnlyMethodsMatchTheCodemod(t *testing.T) {
	wd, err := os.Getwd() // not runtime.Caller: CI builds with -trimpath
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(wd, "..", "..", "scripts", "config_cow_codemod.py"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)READONLY_METHODS = \((.*?)\)`).FindSubmatch(src)
	if block == nil {
		t.Fatal("READONLY_METHODS not found in scripts/config_cow_codemod.py")
	}
	var py []string
	for _, m := range regexp.MustCompile(`"(\w+)"`).FindAllSubmatch(block[1], -1) {
		py = append(py, string(m[1]))
	}
	var gos []string
	for n := range readOnlyMethods {
		gos = append(gos, n)
	}
	sort.Strings(py)
	sort.Strings(gos)
	if !reflect.DeepEqual(py, gos) {
		t.Fatalf("READONLY_METHODS %v != readOnlyMethods %v: add a proof here for every name the codemod trusts", py, gos)
	}
}

// ── the transitional mirror (AppConfig) ──────────────────────────────────────

// TestSetForTestUpdatesGetAndTheMirror: while AppConfig is the transitional
// mirror, a converted test writer must be seen by converted readers (Get) and
// by unconverted ones (AppConfig, Snapshot) alike.
func TestSetForTestUpdatesGetAndTheMirror(t *testing.T) {
	restoreConfig(t)
	SetForTest(func(c *Config) { c.RootDir = "/tmp/gcpd-roof" })
	if Get().RootDir != "/tmp/gcpd-roof" || AppConfig.RootDir != "/tmp/gcpd-roof" || Snapshot().RootDir != "/tmp/gcpd-roof" {
		t.Fatalf("SetForTest reached Get=%q AppConfig=%q Snapshot=%q", Get().RootDir, AppConfig.RootDir, Snapshot().RootDir)
	}
}

// TestADirectMirrorWriteIsFoldedInAtTheNextWrite: an unconverted test that
// still assigns AppConfig directly must not have its write dropped by the next
// publish (every write starts from the mirror).
func TestADirectMirrorWriteIsFoldedInAtTheNextWrite(t *testing.T) {
	restoreConfig(t)
	AppConfig.RootDir = "/tmp/ace-chemicals" // what an unconverted test does
	Mutate(func(c *Config) { c.LogLevel = "debug" })
	if Get().RootDir != "/tmp/ace-chemicals" || Get().LogLevel != "debug" {
		t.Fatalf("the next write dropped the direct mirror write: RootDir=%q LogLevel=%q", Get().RootDir, Get().LogLevel)
	}
}

// TestTheMirrorSharesNothingWithTheSnapshot: an unconverted test that writes
// into the mirror's slices or maps in place must never reach the published
// snapshot.
func TestTheMirrorSharesNothingWithTheSnapshot(t *testing.T) {
	restoreConfig(t)
	SetForTest(func(c *Config) {
		c.SupportedExtensions = []string{".mp3"}
		c.Dedup.Signals.Confidence = map[string]DedupKindConfidence{"isbn": {MinConfidence: 0.9}}
	})
	AppConfig.SupportedExtensions[0] = ".wav"
	AppConfig.Dedup.Signals.Confidence["isbn"] = DedupKindConfidence{MinConfidence: 0.1}
	if Get().SupportedExtensions[0] != ".mp3" || Get().Dedup.Signals.Confidence["isbn"].MinConfidence != 0.9 {
		t.Fatalf("a write into the mirror reached the snapshot: %v %v", Get().SupportedExtensions, Get().Dedup.Signals.Confidence)
	}
	if err := CheckSealed(); err != nil {
		t.Fatal(err)
	}
}

// ── Project (the Facade's rebuild) ───────────────────────────────────────────

// TestProjectPublishesAPrivateNormalisedCopy: the Engine keeps the projection
// it hands Project as its own current value, so the published snapshot must
// share none of its maps or slices, and must be normalised.
func TestProjectPublishesAPrivateNormalisedCopy(t *testing.T) {
	restoreConfig(t)
	cfg := Config{
		DatabaseType:        "pebble",
		RootDir:             "/tmp/wayne-tower",
		SupportedExtensions: []string{".m4b"},
		Dedup:               DedupConfig{Signals: DedupSignalConfig{Confidence: map[string]DedupKindConfidence{"asin": {MinConfidence: 0.7}}}},
	}
	g := Generation()
	Project(cfg)
	if Generation() == g {
		t.Fatal("Project did not move Generation")
	}
	cfg.SupportedExtensions[0] = ".ogg"
	cfg.Dedup.Signals.Confidence["asin"] = DedupKindConfidence{MinConfidence: 0.1}
	got := Get()
	if got.RootDir != "/tmp/wayne-tower" || got.SupportedExtensions[0] != ".m4b" || got.Dedup.Signals.Confidence["asin"].MinConfidence != 0.7 {
		t.Fatalf("the caller's projection reached the published snapshot: %+v", got)
	}
	if AppConfig.SupportedExtensions[0] != ".m4b" {
		t.Fatalf("the caller's projection reached the mirror: %v", AppConfig.SupportedExtensions)
	}
	if got.MinBookSizeBytes != 5*1024*1024 || got.Embedding.VectorBackend != "hnsw" {
		t.Fatalf("Project published an unnormalised config: %+v", got)
	}
}

// TestConcurrentReadersAndProject is the flip-shaped race: a settings save
// rebuilds the projection and Project publishes it while background readers
// read slices and maps off Get(). Clean under -race.
func TestConcurrentReadersAndProject(t *testing.T) {
	restoreConfig(t)
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for !stop.Load() {
				c := Get()
				for _, e := range c.SupportedExtensions {
					_ = e
				}
				_ = c.Dedup.Signals.Confidence["isbn"]
				_ = c.RootDir
			}
		})
	}
	base := Config{DatabaseType: "pebble", Dedup: DedupConfig{Signals: DedupSignalConfig{Confidence: map[string]DedupKindConfidence{}}}}
	for i := range 200 {
		base.SupportedExtensions = append(base.SupportedExtensions, ".m4b")
		base.Dedup.Signals.Confidence["isbn"] = DedupKindConfidence{MinConfidence: float64(i)}
		Project(base) // base keeps changing in place after each publish, like the Engine's next projection
	}
	stop.Store(true)
	wg.Wait()
	if err := CheckSealed(); err != nil {
		t.Fatal(err)
	}
}

// ── NormalizeAndValidate ─────────────────────────────────────────────────────

// TestNormalizeAndValidateIsANoOpOnANormalisedConfig: the books boot step runs
// with a Facade installed, after Project already published a normalised
// projection. It must publish nothing (Generation and the pointer unchanged)
// and must never write through the Facade.
func TestNormalizeAndValidateIsANoOpOnANormalisedConfig(t *testing.T) {
	restoreConfig(t)
	Project(Config{DatabaseType: "pebble", RootDir: "/tmp/batcave"})
	f := &stubFacade{}
	installForTest(t, f)
	before, g := Get(), Generation()
	_ = NormalizeAndValidate()
	if Get() != before || Generation() != g {
		t.Fatalf("NormalizeAndValidate published on an already normalised config (generation %d -> %d)", g, Generation())
	}
	if f.mutated != 0 {
		t.Fatalf("NormalizeAndValidate wrote through the Facade %d times", f.mutated)
	}
}

// TestValidateReportsWhatTheNormalisedConfigBreaks: Validate judges the
// normalised form, so a zero that means "the default" is not a problem, and a
// real problem is still reported.
func TestValidateReportsWhatTheNormalisedConfigBreaks(t *testing.T) {
	if err := (&Config{DatabaseType: "pebble"}).Validate(); err != nil && strings.Contains(err.Error(), "vector_backend") {
		t.Fatalf("a zero vector_backend (the default) was reported: %v", err)
	}
	c := &Config{DatabaseType: "oracle"}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "database_type") {
		t.Fatalf("an invalid database_type was not reported: %v", err)
	}
	if got := c.Problems(); len(got) == 0 {
		t.Fatal("Problems reported nothing for an invalid database_type")
	}
	if c.Embedding.VectorBackend != "" {
		t.Fatal("Problems normalised its receiver")
	}
}
