// file: internal/plugins/maintenance/missing_file_classify_test.go
// version: 1.0.0
// guid: 6c1f0a72-5d84-4b39-8e27-3af10d95b6c2
// last-edited: 2026-08-19

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeriveTrackSlashCandidates pins the derivation the whole classification
// rests on. It is a regex over paths, so the cases that matter are the ones that
// must NOT match: a false positive here would report a row as recoverable and
// point at a file that is not its bytes.
func TestDeriveTrackSlashCandidates(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    []string
		matched bool
	}{
		{
			name:    "two-digit track needs no padding, one candidate",
			path:    "/lib/Blue Ant 3/Zero History - 70/131.mp3",
			want:    []string{"/lib/Blue Ant 3/Zero History - 70.mp3"},
			matched: true,
		},
		{
			name: "one-digit track is ambiguous, padded first then unpadded",
			path: "/lib/Blue Ant 3/Zero History - 7/131.mp3",
			want: []string{
				"/lib/Blue Ant 3/Zero History - 07.mp3",
				"/lib/Blue Ant 3/Zero History - 7.mp3",
			},
			matched: true,
		},
		{
			name:    "already flat is not a track-slash row",
			path:    "/lib/Blue Ant 3/Zero History - 70.mp3",
			matched: false,
		},
		{
			name:    "non-numeric leaf is not the phantom total-track file",
			path:    "/lib/Blue Ant 3/Zero History - 70/cover.mp3",
			matched: false,
		},
		{
			name:    "numeric leaf but parent has no track suffix",
			path:    "/lib/Blue Ant 3/Zero History/131.mp3",
			matched: false,
		},
		{
			name:    "no extension is not a file we can derive",
			path:    "/lib/Blue Ant 3/Zero History - 70/131",
			matched: false,
		},
		{
			name:    "a title ending in a number is not a track suffix without the dash",
			path:    "/lib/Blue Ant 3/Zero History 70/131.mp3",
			matched: false,
		},
		{
			name:    "m4b extension is carried through, not assumed mp3",
			path:    "/lib/Book/Title - 12/99.m4b",
			want:    []string{"/lib/Book/Title - 12.m4b"},
			matched: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := deriveTrackSlashCandidates(tc.path)
			if matched != tc.matched {
				t.Fatalf("matched = %v, want %v (candidates %v)", matched, tc.matched, got)
			}
			if !tc.matched {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d candidate(s) %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("candidate[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestClassifyMissingRows_CountsAgainstRealFiles runs the classifier against a
// real temp tree rather than a stubbed stat. The three outcomes are exercised at
// once so a change that collapses two of them into each other cannot pass.
func TestClassifyMissingRows_CountsAgainstRealFiles(t *testing.T) {
	root := t.TempDir()
	bookDir := filepath.Join(root, "Blue Ant 3")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The recoverable one: its bytes are on disk under the flat name.
	flat := filepath.Join(bookDir, "Zero History - 70.mp3")
	if err := os.WriteFile(flat, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	missing := []string{
		filepath.Join(bookDir, "Zero History - 70/131.mp3"), // recoverable
		filepath.Join(bookDir, "Zero History - 71/131.mp3"), // shape, but no bytes
		filepath.Join(bookDir, "Zero History.mp3"),          // no shape at all
	}

	// Point the controls at paths under the temp root that are never created, so
	// the instrument check runs for real instead of relying on /mnt not existing
	// on a developer's machine.
	orig := missingFileClassifyControls
	missingFileClassifyControls = []string{filepath.Join(root, "__CONTROL_MUST_BE_MISSING__.mp3")}
	t.Cleanup(func() { missingFileClassifyControls = orig })

	got, err := classifyMissingRows(context.Background(), missing, 10, &fakeReporter{})
	if err != nil {
		t.Fatalf("classifyMissingRows: %v", err)
	}

	if got.Recoverable != 1 {
		t.Errorf("Recoverable = %d, want 1", got.Recoverable)
	}
	if got.ShapeNoBytes != 1 {
		t.Errorf("ShapeNoBytes = %d, want 1", got.ShapeNoBytes)
	}
	if got.NoShape != 1 {
		t.Errorf("NoShape = %d, want 1", got.NoShape)
	}
	if got.RecoveredByPadded != 1 || got.RecoveredByUnpadded != 0 {
		t.Errorf("padded=%d unpadded=%d, want 1/0 — a two-digit track has exactly one candidate",
			got.RecoveredByPadded, got.RecoveredByUnpadded)
	}
	if got.ControlsPlanted != 1 || got.ControlsResolved != 0 {
		t.Errorf("controls planted=%d resolved=%d, want 1/0", got.ControlsPlanted, got.ControlsResolved)
	}
	if len(got.SampleRecoverable) != 1 || !strings.Contains(got.SampleRecoverable[0], flat) {
		t.Errorf("SampleRecoverable = %v, want one entry naming %q", got.SampleRecoverable, flat)
	}
}

// TestClassifyMissingRows_ResolvedControlFailsTheRun is the reason the controls
// exist. A stat that answers yes to a path that cannot exist invalidates every
// recoverable verdict in the run, so the run must FAIL rather than publish a
// number with a caveat nobody reads.
func TestClassifyMissingRows_ResolvedControlFailsTheRun(t *testing.T) {
	root := t.TempDir()
	planted := filepath.Join(root, "__CONTROL_MUST_BE_MISSING__.mp3")
	if err := os.WriteFile(planted, []byte("this must never exist"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := missingFileClassifyControls
	missingFileClassifyControls = []string{planted}
	t.Cleanup(func() { missingFileClassifyControls = orig })

	_, err := classifyMissingRows(context.Background(),
		[]string{filepath.Join(root, "Book/Title - 3/9.mp3")}, 10, &fakeReporter{})
	if err == nil {
		t.Fatal("classifyMissingRows returned nil error with a resolved control — " +
			"the instrument check is inert and every recoverable count it reports is unverified")
	}
	if !strings.Contains(err.Error(), "instrument check failed") {
		t.Errorf("error = %q, want it to name the instrument check", err)
	}
}
