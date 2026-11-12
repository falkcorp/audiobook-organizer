// file: core/backup/space_internal_test.go
// version: 1.0.0
// guid: ff438145-7358-41b2-a0e6-168f2d12a0af
// last-edited: 2026-11-12

package backup

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoDisks is a Disk of two devices: dirs under "/a" are on device 1 with availA
// bytes free, every other dir is on device 2 with availB.
func twoDisks(availA, availB uint64) Disk {
	onA := func(p string) bool { return strings.HasPrefix(p, "/a") }
	return Disk{
		Device: func(p string) (uint64, error) {
			if onA(p) {
				return 1, nil
			}
			return 2, nil
		},
		Avail: func(p string) (uint64, error) {
			if onA(p) {
				return availA, nil
			}
			return availB, nil
		},
	}
}

func oneDisk(avail uint64) Disk {
	return Disk{
		Device: func(string) (uint64, error) { return 1, nil },
		Avail:  func(string) (uint64, error) { return avail, nil },
	}
}

// Two directories on one device share its free space: the claims add up and are
// checked against it once, margin included. Checking each against the whole of it
// would promise the same bytes twice.
func TestRequireSpaceSumsClaimsOnOneDevice(t *testing.T) {
	claims := []spaceClaim{{"/a/stage", 60}, {"/a/out", 40}}
	if err := requireSpace(oneDisk(100), 0, claims...); err != nil {
		t.Fatalf("exactly enough: %v", err)
	}
	if err := requireSpace(oneDisk(99), 0, claims...); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("one byte short = %v, want ErrInsufficientSpace", err)
	}
	if err := requireSpace(oneDisk(100), 1, claims...); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("the margin is part of the need: %v", err)
	}
	if err := requireSpace(oneDisk(101), 1, claims...); err != nil {
		t.Fatalf("claims and margin exactly: %v", err)
	}
}

// Claims on different devices are checked against their own device, each with
// the margin.
func TestRequireSpaceChecksEachDeviceOnItsOwn(t *testing.T) {
	claims := []spaceClaim{{"/a/stage", 50}, {"/b/out", 10}}
	if err := requireSpace(twoDisks(50, 10), 0, claims...); err != nil {
		t.Fatalf("each device exactly enough: %v", err)
	}
	if err := requireSpace(twoDisks(1000, 9), 0, claims...); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("the second device one byte short = %v, want ErrInsufficientSpace", err)
	}
	if err := requireSpace(twoDisks(49, 1000), 0, claims...); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("the first device one byte short = %v, want ErrInsufficientSpace", err)
	}
	// The margin is kept on every device, not once.
	if err := requireSpace(twoDisks(50, 10), 1, claims...); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("a margin on each device = %v, want ErrInsufficientSpace", err)
	}
	if err := requireSpace(twoDisks(51, 11), 1, claims...); err != nil {
		t.Fatalf("both devices with their margin: %v", err)
	}
}

func TestRequireSpaceRefusesWhatOverflows(t *testing.T) {
	huge := oneDisk(math.MaxUint64)
	if err := requireSpace(huge, 0, spaceClaim{"/a/x", math.MaxUint64}, spaceClaim{"/a/y", 1}); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("claims that overflow a uint64 = %v, want ErrInsufficientSpace", err)
	}
	if err := requireSpace(huge, 1, spaceClaim{"/a/x", math.MaxUint64}); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("a claim and margin that overflow = %v, want ErrInsufficientSpace", err)
	}
	if err := requireSpace(huge, 0, spaceClaim{"/a/x", math.MaxUint64}); err != nil {
		t.Fatalf("a claim that exactly fits the largest count: %v", err)
	}
}

// Space that cannot be measured is not assumed: a failed device or free-space
// query fails the check, with the cause kept and without being taken for a
// shortage.
func TestRequireSpaceFailsClosedWhenItCannotMeasure(t *testing.T) {
	boom := errors.New("batcave sensor offline")
	deviceFails := Disk{Device: func(string) (uint64, error) { return 0, boom }, Avail: func(string) (uint64, error) { return math.MaxUint64, nil }}
	if err := requireSpace(deviceFails, 0, spaceClaim{"/a/x", 1}); !errors.Is(err, boom) || errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("a failed device query = %v, want the cause and not a shortage", err)
	}
	availFails := Disk{Device: func(string) (uint64, error) { return 1, nil }, Avail: func(string) (uint64, error) { return 0, boom }}
	if err := requireSpace(availFails, 0, spaceClaim{"/a/x", 1}); !errors.Is(err, boom) || errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("a failed free-space query = %v, want the cause and not a shortage", err)
	}
}

// The error reaches an API caller as a fixed body, but it also reaches logs and
// the CLI: it carries numbers, never a directory.
func TestRequireSpaceErrorNamesNoPath(t *testing.T) {
	const secretDir = "/a/operator/private/state"
	err := requireSpace(oneDisk(1), 0, spaceClaim{secretDir, 5})
	if !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "operator") || strings.Contains(err.Error(), "/") {
		t.Fatalf("the error names a path: %v", err)
	}
	if !strings.Contains(err.Error(), "1 bytes free") || !strings.Contains(err.Error(), "5 needed") {
		t.Fatalf("the error does not say how much: %v", err)
	}
}

func TestRequireSpaceWithNoClaimsAsksNothing(t *testing.T) {
	asked := false
	d := Disk{
		Device: func(string) (uint64, error) { asked = true; return 0, nil },
		Avail:  func(string) (uint64, error) { asked = true; return 0, nil },
	}
	if err := requireSpace(d, 1<<40); err != nil || asked {
		t.Fatalf("err = %v, asked = %v; with nothing to write there is nothing to measure", err, asked)
	}
}

func TestSettingsMarginDefaultsToTwoGiB(t *testing.T) {
	if got := (Settings{}).margin(); got != 2<<30 {
		t.Fatalf("the default margin = %d, want 2 GiB", got)
	}
	if got := (Settings{SpaceMargin: 7}).margin(); got != 7 {
		t.Fatalf("a configured margin = %d, want 7", got)
	}
}

func TestDeclaredBytes(t *testing.T) {
	ents := []Entry{{Path: "db/1.sst", Size: 10}, {Path: "db/2.sst", Size: 20}, {Path: "config/x.yaml", Size: 5}, {Path: "dbx/3", Size: 100}}
	for prefix, want := range map[string]uint64{"": 135, "db/": 30, "config/": 5, "none/": 0} {
		got, err := declaredBytes(ents, prefix)
		if err != nil || got != want {
			t.Errorf("declaredBytes(%q) = %d, %v; want %d", prefix, got, err, want)
		}
	}
	if _, err := declaredBytes([]Entry{{Path: "db/1", Size: -1}}, ""); !errors.Is(err, ErrManifest) {
		t.Errorf("a negative size = %v, want ErrManifest", err)
	}
	over := []Entry{{Path: "db/1", Size: math.MaxInt64}, {Path: "db/2", Size: math.MaxInt64}, {Path: "db/3", Size: 2}}
	if _, err := declaredBytes(over, ""); !errors.Is(err, ErrManifest) {
		t.Errorf("sizes that overflow = %v, want ErrManifest", err)
	}
	// A negative size outside the prefix is not what the pass writes.
	if got, err := declaredBytes([]Entry{{Path: "db/1", Size: 3}, {Path: "other/1", Size: -1}}, "db/"); err != nil || got != 3 {
		t.Errorf("declaredBytes ignoring other/ = %d, %v", got, err)
	}
}

func TestBudgetOfAndAddBytes(t *testing.T) {
	for in, want := range map[uint64]int64{0: 0, 5: 5, math.MaxInt64: math.MaxInt64, math.MaxInt64 + 1: math.MaxInt64, math.MaxUint64: math.MaxInt64} {
		if got := budgetOf(in); got != want {
			t.Errorf("budgetOf(%d) = %d, want %d", in, got, want)
		}
	}
	if sum, ok := addBytes(2, 3); !ok || sum != 5 {
		t.Errorf("addBytes(2,3) = %d, %v", sum, ok)
	}
	if _, ok := addBytes(math.MaxUint64, 1); ok {
		t.Error("addBytes past the largest count reported success")
	}
}

// A stale <name>.tmp (a crashed write's) must not make the next write of the
// same name fail or be overwritten: the temp name is unique to the call.
func TestWriteFileAtomicIgnoresAStaleTempFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gotham_20261112_020000.tar.gz.sha256")
	stale := p + ".tmp"
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("fresh")); err != nil {
		t.Fatalf("writeFileAtomic with a stale temp file beside it: %v", err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "fresh" {
		t.Fatalf("content = %q, %v", b, err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	if b, err := os.ReadFile(stale); err != nil || string(b) != "stale" {
		t.Fatalf("the stale file was disturbed: %q, %v", b, err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 2 {
		t.Fatalf("directory = %v, %v; want the file and the untouched stale one only", ents, err)
	}
}

// Two writes of one name leave one file and no temporary one.
func TestWriteFileAtomicLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.verify.json")
	for _, body := range []string{"one", "two"} {
		if err := writeFileAtomic(p, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 1 || ents[0].Name() != "x.verify.json" {
		t.Fatalf("directory = %v, %v", ents, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("content = %q", b)
	}
}
