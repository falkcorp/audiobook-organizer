// file: internal/config/cow_test.go
// version: 1.0.0
// guid: a5cb8168-720c-4fc4-b90f-b851c33ab322
// last-edited: 2026-11-21

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

// restoreConfig puts the snapshot back after a test that changes it.
func restoreConfig(t *testing.T) {
	t.Helper()
	saved := *Get().Clone()
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
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				c := Get()
				_ = len(c.SupportedExtensions)
				for _, e := range c.SupportedExtensions {
					_ = e
				}
				_ = c.Dedup.Signals.Confidence["isbn"]
				_ = c.RootDir
			}
		}()
	}
	for i := 0; i < 200; i++ {
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
