// file: core/coretest/crashmatrix/doc.go
// version: 1.0.2
// guid: 144093b3-6f51-4e66-92f8-51a3933e7804
// last-edited: 2026-11-08

// Package crashmatrix is the boot crash-matrix kit (05-storage §5S.3.16; 06
// §6.4.8, §6.5.1 "Boot writers and the crash matrix"): it breaks an
// in-process boot at every durable write, rename and fsync between P0a and
// readiness, in two modes, and checks what the next boots of the current
// build make of it.
//
//   - Kill: a crash just before the operation. The kit runs the boot once
//     over a crashable in-memory copy of the fixture (the census) and takes
//     a crash clone (no unsynced data) before every kill point, then boots
//     each clone. A write collapses into the next sync of its file.
//   - Error: the one operation returns ENOSPC (a second run: EACCES) and the
//     boot goes on, so a failure that is not a crash must leave a legal
//     state too (R10-6, DATA10-4). Pebble's own files are kill points only
//     (decision D12): Pebble defines their failure as fatal, so the kill at
//     the same operation already covers the state it leaves, and an error
//     there panics in a goroutine no recover reaches. Pebble's own crash
//     safety is relied on, not re-proven.
//
// After each broken boot the kit boots the current build again, up to three
// boots in all (a boot that swaps a failed restore back exits, and the
// restart boots the original store), and asserts the invariants of
// 05-storage §5S.3.16 that exist at R1 (invariants.go).
//
// The boots after a break run recovery writers no fault-free boot reaches:
// step x after a failed P0a, a reset's move-back, a rejected reset, the slot
// cleanup, and every step that finds its work done by a boot whose fsync
// failed. So those are broken too, one level deep (R1-21 review M6): the
// first reboot after each kill, and the rest of the boot after each
// injected error with its first reboot, are killed again before each of
// their kill points that carries a swap label (swap.*), and each nested
// kill is booted to readiness and checked like any other. A nested run is
// not broken again. A fixture with a staged swap boots roughly ten to
// thirty times as often as it would without them.
//
// Every durable operation carries the label of the boot writer that made it
// (internal/bootseam's regions, set at each writer's call site), and the
// label must be one of the writer table's rows (Rows): an unlabelled
// operation, or a label no row lists, fails the run. After each fixture the
// kit also compares the real directories the fixture names with what they
// held before, so a writer that bypassed the boot filesystem and wrote to
// the real disk fails too (COR10-7).
//
// Only the operations of a fixture's fault-free boot are broken, so a label
// no fixture's fault-free boot reaches is a writer the matrix never faults;
// Unobserved names every such label (TestBootCrashMatrixCoversEveryWriter),
// and an app lists the ones it cannot reach with the reason. Fixture.Mutate
// and KillAt make the starting states staging cannot, such as a boot killed
// part way. Two limits remain (R1-21 review M4, m9):
//
//   - A writer added inside an existing labelled region takes that region's
//     label: its operations are broken like the region's own, but no check
//     sees that it has no row. Give every new writer its own region.
//   - The Recorder holds one current label per boot, not per goroutine, and
//     the outer region's label wins. A boot that runs two labelled writers
//     at once gives both writers' operations the label of the region opened
//     first; boot writers must stay sequential until the labeller is made
//     per goroutine.
//
// The kit boots through Injector.New, which is app.New with the boot's
// filesystem, binary ID, clock and labeller wired in through internal/
// bootseam; no core/internal type is exposed. It is test support: no
// non-test core package imports it.
package crashmatrix
