// file: internal/plugins/maintenance/native_scan_guard.go
// version: 0.1.0
// guid: 6c0e8b3a-d417-4e92-b5f8-3a1d9e72c604
// last-edited: 2026-11-22

package maintenance

import "github.com/falkcorp/mediacore/tasks"

// nativeScanNotWhile is the dispatch-time form of HEAD's "no library.scan
// queued or running" refusal (refuseWhileLibraryScanActive), for the native
// kinds that rewrite rows a scan rewrites: they stay in waiting_deps (reason
// not_while) while either scan kind runs, holding no worker, no key and no
// clock, and start when it ends. books.library.scan-full is the full sweep's own
// kind and shares the scan's body and key (05-tasks 5T.8.2 rename map rule 4), so
// a guard that named only books.library.scan would let a native run start beside
// a full sweep. The slice is a fresh copy so a definition cannot alias another's.
//
// What NotWhile does not cover is a scan that is only queued: it blocks while a
// scan is running, where HEAD's read also refused on a queued one. What stops a
// scan that starts later is the scan stand-down hold the native live apply takes
// before its first write (rc.StandDown over the owner's scan key). Each kind this
// guard is wired on takes one by its own path: folder-cover-backfill
// (t6g3NativeStandDown), fs-regroup-xml (nativeScanStandDown),
// itunes-clone-into-library (t6g5NativeLease) and repoint-missing-to-folder-audio
// (t6g7NativeStandDown). The HEAD shells' queue read stays as it is until the
// flip deletes it.
//
// Literals, not kindrename.ScanKind and FullSweepKind: this package may not
// import the cutover tree. TestNativeScanNotWhile_PinnedToTheRenameMap holds the
// two together.
func nativeScanNotWhile() []tasks.Kind {
	return []tasks.Kind{"books.library.scan", "books.library.scan-full"}
}
