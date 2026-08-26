<!-- file: docs/audits/current-status-evidence/2026-08-25-imported-scan-forensics-handoff.md -->
<!-- version: 1.1.0 -->
<!-- guid: f898b036-570c-4d52-87cb-1241530d8eae -->
<!-- last-edited: 2026-08-25 -->

# Imported Scan Forensics Handoff — 2026-08-25

## Provenance and scope

This is a preservation of the user-provided cross-agent handoff in this
conversation.  The user confirmed it is the most up-to-date information for
this audit.  Its historical census values still need re-measurement before a
repair is applied, but its current production state supersedes earlier,
conflicting snapshots in this audit.

## Confirmed historical root-cause chain

At the time of the investigation, production had
`chapter_consolidation_threshold_min = 0`; the intended default is 10 and zero
explicitly disables consolidation.  The verified chain was:

1. Album-less multi-file books fall through to chapter consolidation.
2. With a threshold less than or equal to zero, the fallback returns one Book
   per file rather than a grouped Book with multiple `SegmentFiles`.
3. Each resulting single-file candidate bypasses the former `len(SegmentFiles)
   > 1` BookFile-creation gate.
4. Existing tagged rows can be repeatedly re-linked from track to track; other
   tracks become separate fragment rows.

The report measured a ground-truth sample with 223 of 224 audio files lacking
an album tag, while track numbers were present.  It also recorded scanner logs
showing one Book ID being re-linked across approximately 96 track paths.  This
is creation-side evidence, not merely a potentially shared read-path result.

## Historical scope and repair implications

The handoff recorded a census of **12,525 books with no file rows out of 61,490
(20.4%)**, with 545,932 total book-file rows; it separately named 1,710
track-titled fragments.  The count needs a new live measurement before it is
used operationally, including confirmation of soft-delete treatment.

Changing the threshold fixes future scans only.  It does not repair rows and
fragments already written.  Existing damaged data needs a dry-run-first
backfill/repair plan that checks every downstream eligibility gate before
claiming success.

## Configuration persistence finding

The investigation found that the file-backed configuration allowlist does not
persist this threshold, while the database save path serializes a full config
structure.  That makes accidental zero persistence plausible, but the handoff
did **not** prove when or how the production value changed.  It also found the
authenticated configuration update path merges supplied fields into the
existing configuration rather than replacing the whole configuration.

Follow-up needed: harden persistence and startup observability so an unintended
zero cannot silently disable consolidation again.  This is a separate design
and implementation task; do not change it by broad serialization edits.

## Subsequent fixes recorded by the handoff

- PR #2929: import/organize wiring, explicit UI default off, BookFile creation
  and the additional resolved-author eligibility gate.  Reported merged with
  green CI.
- PR #2930: end-to-end directory scan regression test proving BookFile rows
  exist; reported merged as `c5edf0c9c`.
- A CI hygiene issue remains: CI checks out Git LFS pointers rather than real
  fixture audio.  The reported safe order is: make the shared fixture helper
  fail on a pointer, enable LFS checkout, then repair tests that surface.  Do
  not enable LFS first and treat resulting broad failures as regressions.

## Open threads carried forward

1. Re-measure current no-file and fragment counts before repair work.
2. Design and dry-run the repair for historical missing BookFile rows and
   fragments; do not apply it during an active scan.
3. Confirm why/when the threshold was zeroed and harden persistence/visibility.
4. Fix the CI LFS-fixture hole using the staged order above.
5. The user's newer requirement still stands: metadata candidates must remain
   durably pending while the LLM is unavailable, not merely be retried by a
   later scan.
