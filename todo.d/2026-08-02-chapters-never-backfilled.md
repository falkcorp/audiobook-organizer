<!-- file: todo.d/2026-08-02-chapters-never-backfilled.md -->
<!-- version: 1.0.0 -->
<!-- guid: c8d0451f-72a9-4e63-b514-9f3e6a07c2d8 -->
<!-- last-edited: 2026-08-02 -->

## MISSING: no book in the library has stored chapters — extraction only ever runs during a scan, and no scan has run

Reported by the owner 2026-08-02: "don't we extract chapters from the files that have
them and then use the tracks for others? I'm not seeing the chapters in the app."

The extraction code **is** implemented and correct. It has simply never run against the
existing library.

### Evidence chain (all four links verified 2026-08-02)

1. **`SaveChaptersForBook` has exactly one caller:**
   `scanner.PersistChaptersForBook` (`internal/scanner/process_file.go:259`).
2. **That function is only invoked from a scan** — `internal/scanner/scanner.go:851`
   and `:1035`, both inside the per-book scan worker. Nothing else calls it.
3. **`library.scan` has not run in 14 days.** All 31 occurrences of `id=library.scan`
   in the journal are the op-*registration* line emitted at startup; there are zero
   run records. **There is also no chapter backfill op** — no registered op id
   contains "chapter" except the unrelated `dedup.quarantine-chapter-artifacts`.
   (Phase 4 of the ABS spec called for a `registry.RunItems` backfill; it was never
   built.)
4. **So `GetChaptersForBook` always returns empty**, and
   `abs/mapper.go:loadChapters` falls through to synthesizing chapters on the fly.

### 🔑 The important part: a backfill only helps SINGLE-FILE books

This is the non-obvious bit, and it decides whether a backfill is worth building.

| Book shape | Stored (scan) path | Live fallback (today) | Visible difference |
|---|---|---|---|
| **single-file** (m4b w/ embedded markers) | `probeSingleFileChapters` → the file's **real** embedded chapters | `SynthesizeChapters` over 1 track → **one** chapter for the whole book | 🔴 **Large.** 6 real chapters vs. 1. |
| **multi-file** (mp3 set) | `synthesizeMultiFileChapters` → `SynthesizeChapters`, one per file | `SynthesizeChapters`, one per file | ⚪ **None.** Same count, same titles; only sub-second boundaries differ (re-probed unrounded duration vs. stored `DurationSec`). |

Both paths call the **same** `audioutil.SynthesizeChapters`. So for a multi-file book a
backfill is a no-op as far as the user can see.

⚠️ **The book the owner was actually playing (`44669fab-6544-4414-ae2d-fa8eba7c52f3`)
is multi-file** — production traffic shows it streaming `/public/session/…/track/1`
and `/track/2`. **A backfill would change nothing for that book.**

### Decision needed

1. **Populate chapters** — pick one:
   - run `library.scan` (populates as a side effect, but does a great deal else, and
     has not run in 14 days for reasons nobody has written down); or
   - build the dedicated bounded-pool backfill op the Phase 4 spec called for
     (`registry.RunItems`, one ffprobe per single-file book).
   Either way, scope it to **single-file books** — that is where the entire visible
   gain is, and it avoids ~40k pointless ffprobe calls.
2. **Decide whether multi-file books should use their per-file embedded chapters.**
   `synthesizeMultiFileChapters` deliberately ignores them ("never from that file's own
   embedded sub-chapters, even when present — real ABS ground truth, spec §1.8.5").
   `audioutil.ShiftChapters` exists precisely to rebase them onto the whole-book
   timeline and is **unused** on this path. If the owner wants real chapters inside a
   multi-file audiobook, that is a **separate feature**, not a backfill — and it means
   deliberately diverging from real-ABS behaviour.

**Do not run a whole-library backfill without answering (1) first** — a scan touches
far more than chapters.
