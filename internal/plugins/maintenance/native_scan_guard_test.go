// file: internal/plugins/maintenance/native_scan_guard_test.go
// version: 0.1.0
// guid: 91d4a7e2-3b58-4c6f-a0e9-5f27b8c13d46
// last-edited: 2026-11-22

package maintenance

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/parity"
)

// TODO-F5-18-FOLDER-COVER-SCAN-GUARD and TODO-F5-09-RESUMED-INTRO-SCAN-HOLD
// (F5-18e): the native kinds that rewrite rows a scan rewrites are held at
// dispatch while a scan runs. The HEAD shells keep their v2 queue read (the flip
// deletes it), so none of this is reachable from ABO's boot: no native kind is
// registered on main.

// nativeScanGuardCase is one guarded native kind, built by its own constructor
// with no deps (the held run never reaches its body).
type nativeScanGuardCase struct {
	name string
	kind tasks.Kind
	def  func() tasks.Definition
	// want is the NotWhile the kind must carry.
	want []tasks.Kind
}

func nativeScanGuardCases() []nativeScanGuardCase {
	p := &Plugin{}
	scans := []tasks.Kind{kindrename.ScanKind, kindrename.FullSweepKind}
	return []nativeScanGuardCase{
		{"folder-cover-backfill", t6g3FolderCoverBackfillKind, func() tasks.Definition { return p.folderCoverBackfillKind(t6g3Deps{}, nil) }, scans},
		{"fs-regroup-xml", fsRegroupKind, func() tasks.Definition { return p.fsRegroupXMLKind(t6g4Deps{}, nil) }, scans},
		{"itunes-clone-into-library", itunesCloneIntoLibraryKind, func() tasks.Definition { return p.itunesCloneIntoLibraryKind(t6g5Deps{}, nil) }, scans},
		{"repoint-missing-to-folder-audio", rfKind, func() tasks.Definition { return p.repointMissingToFolderAudioNative(t6g7Deps{}, nil) }, scans},
		{"transcribe-file-intros", fileIntroKind, func() tasks.Definition { return p.fileIntroTranscribeKind(t6g4Deps{}, nil) }, fileIntroBusyKinds()},
	}
}

// fileIntroBusyKinds is fileIntroBusyOps through the rename map (a kind per def
// ID), plus the full sweep, which shares the scan's body and key.
func fileIntroBusyKinds() []tasks.Kind {
	var out []tasks.Kind
	for id := range fileIntroBusyOps {
		tgt, ok := kindrename.Lookup(id)
		if !ok {
			panic("fileIntroBusyOps names " + id + ", which the rename map does not know")
		}
		out = append(out, tgt.Kind)
	}
	out = append(out, kindrename.FullSweepKind)
	slices.Sort(out)
	return out
}

func sortedKinds(in []tasks.Kind) []tasks.Kind {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// The guard literals are the rename map's kinds. A kind renamed there, or an op
// added to fileIntroBusyOps, fails here instead of leaving a native kind unguarded.
func TestNativeScanNotWhile_PinnedToTheRenameMap(t *testing.T) {
	require.Equal(t, []tasks.Kind{kindrename.ScanKind, kindrename.FullSweepKind}, nativeScanNotWhile())
	require.Equal(t, fileIntroBusyKinds(), sortedKinds(fileIntroNativeNotWhile()),
		"fileIntroNativeNotWhile must be fileIntroBusyOps as kinds plus the full sweep")
	// Each call is a fresh slice: a definition cannot alias another's guard.
	a, b := nativeScanNotWhile(), nativeScanNotWhile()
	a[0] = "books.gotham.mutated"
	require.NotEqual(t, a[0], b[0])
}

// Every guarded native kind declares the guard.
func TestNativeScanNotWhile_EveryGuardedNativeKindDeclaresIt(t *testing.T) {
	for _, c := range nativeScanGuardCases() {
		t.Run(c.name, func(t *testing.T) {
			def := c.def()
			require.Equal(t, c.kind, def.Kind())
			require.Equal(t, sortedKinds(c.want), sortedKinds(tasks.CommonOf(def).NotWhile),
				"%s must wait while a scan runs", c.kind)
		})
	}
}

// A real executor holds each guarded kind while a scan runs, and releases it
// when the scan ends. The intro run is planted as a continued attempt (attempt 1,
// queued again after a yield): behind a scan it stays in waiting_deps with the
// attempt count it had, so no attempt clock is burned (TODO-F5-09-RESUMED-INTRO-SCAN-HOLD).
// Mutants: NotWhile removed from a kind (that case's run is dispatched while
// the scan runs); the scan kind dropped from the list (the same).
func TestNativeScanNotWhile_ARealExecutorHoldsEachKindWhileAScanRuns(t *testing.T) {
	for _, c := range nativeScanGuardCases() {
		t.Run(c.name, func(t *testing.T) {
			k := parity.NewKit(t)
			scan := k.Probe(string(kindrename.ScanKind)).WaitsRelease()
			k.Boot(parity.Defs(c.def(), scan.Def()))

			attempt := 0
			if c.kind == fileIntroKind {
				attempt = 1 // a continued attempt
			}
			// The scan is running before the run is queued, so there is no window in
			// which dispatch could start the run first.
			scanID := k.Start(kindrename.ScanKind, nil)
			k.WaitStarted(scan)
			id := k.Plant(c.kind, tasks.StateQueued, func(r *tasks.RunRecord) { r.Attempt = attempt })

			// The scan is running: the run waits, holding no attempt.
			k.WaitState(id, tasks.StateWaitingDeps)
			k.Stays(id, tasks.StateWaitingDeps, 600*time.Millisecond)
			rec := k.Get(id)
			require.Equal(t, "not_while", rec.Reason)
			require.Equal(t, attempt, rec.Attempt, "a held run burns no attempt")

			// The scan ends: the hold lifts and the run is dispatched. Its body has
			// no deps here and fails, which is fine; leaving waiting_deps is the point.
			scan.Free()
			k.WaitState(scanID, tasks.StateSucceeded)
			require.Eventually(t, func() bool { return k.Get(id).State != tasks.StateWaitingDeps }, 30*time.Second, 50*time.Millisecond,
				"the run stayed held after the scan ended")
		})
	}
}
