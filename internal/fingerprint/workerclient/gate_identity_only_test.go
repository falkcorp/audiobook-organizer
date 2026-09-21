// file: internal/fingerprint/workerclient/gate_identity_only_test.go
// version: 1.0.0
// guid: 7c81f0a2-4e3d-4a19-9b62-5d0c8ab7e341
// last-edited: 2026-09-21

package workerclient

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
)

func gateWorker() *worker {
	w := &worker{roots: map[string]string{"libroot": "/mnt/lib", "books": "/mnt/books"}}
	w.cfg.Versions.Fpcalc = "1.6.1"
	w.cfg.Versions.FFmpeg = "9.0.2"
	return w
}

func withWindows(root string) workerapi.CalibrationFile {
	return workerapi.CalibrationFile{Root: root, Rel: root + "/a.m4b", Windows: []workerapi.CalibrationWindow{{RawSHA256: "deadbeef", Frames: 12}}}
}

func identityOnly(root string) workerapi.CalibrationFile {
	return workerapi.CalibrationFile{Root: root, Rel: root + "/b.m4b", IdentityOnly: true, Windows: []workerapi.CalibrationWindow{}}
}

// TestCalibrationTargets_AcceptsIdentityOnlyForUnprovenRoot pins the fix for
// the 2026-09-21 deadlock. The server offers a windowless file for a root it
// has no reference window under yet; the worker used to drop it (the
// len(Windows)==0 test) and then refuse to start because that root was
// unproven — so the root could never get windows and could never be proven.
func TestCalibrationTargets_AcceptsIdentityOnlyForUnprovenRoot(t *testing.T) {
	w := gateWorker()
	h := &workerapi.HelloResponse{Calibration: []workerapi.CalibrationFile{
		withWindows("libroot"), identityOnly("books"),
	}}
	cfs, bootstrap, err := w.calibrationTargets(h)
	if err != nil {
		t.Fatalf("gate refused a mapping-only root: %v", err)
	}
	if bootstrap {
		t.Error("bootstrap should be false: libroot has real reference windows")
	}
	roots := map[string]bool{}
	for _, cf := range cfs {
		roots[cf.Root] = true
	}
	if !roots["libroot"] || !roots["books"] {
		t.Errorf("both roots must be covered, got %v", roots)
	}
}

// A windowless file with NEITHER the IdentityOnly marker nor a bootstrap
// response is malformed and must still be dropped — the marker is what carries
// the weaker claim, not the empty slice.
func TestCalibrationTargets_DropsUnmarkedWindowlessFile(t *testing.T) {
	w := gateWorker()
	bare := identityOnly("books")
	bare.IdentityOnly = false
	h := &workerapi.HelloResponse{Calibration: []workerapi.CalibrationFile{withWindows("libroot"), bare}}
	if _, _, err := w.calibrationTargets(h); !errors.Is(err, ErrParity) {
		t.Fatalf("want ErrParity for an unmarked windowless file, got %v", err)
	}
}

// Pipeline parity must never be waived by accident: if every offered file is
// mapping-only and this is not a bootstrap, there is nothing to reproduce and
// the worker refuses rather than running unchecked.
func TestCalibrationTargets_RefusesWhenNothingCarriesParity(t *testing.T) {
	w := gateWorker()
	h := &workerapi.HelloResponse{Calibration: []workerapi.CalibrationFile{
		identityOnly("libroot"), identityOnly("books"),
	}}
	_, _, err := w.calibrationTargets(h)
	if !errors.Is(err, ErrParity) {
		t.Fatalf("want ErrParity, got %v", err)
	}
	if !strings.Contains(err.Error(), "pipeline parity cannot be checked") {
		t.Errorf("error should name the missing parity reference, got: %v", err)
	}
}

// A bootstrap response is all identity-only by definition and must pass.
func TestCalibrationTargets_BootstrapPassesAllIdentity(t *testing.T) {
	w := gateWorker()
	h := &workerapi.HelloResponse{
		Bootstrap:      true,
		ReferenceTools: &workerapi.ToolVersions{Fpcalc: "1.6.1", FFmpeg: "9.0.2"},
		Calibration:    []workerapi.CalibrationFile{identityOnly("libroot"), identityOnly("books")},
	}
	cfs, bootstrap, err := w.calibrationTargets(h)
	if err != nil {
		t.Fatalf("bootstrap gate refused: %v", err)
	}
	if !bootstrap || len(cfs) != 2 {
		t.Errorf("want bootstrap with 2 files, got bootstrap=%v n=%d", bootstrap, len(cfs))
	}
}
