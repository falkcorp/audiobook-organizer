- [ ] **SCAN-PHASE** Restructure the library scan into discrete, resumable phases —
      owner report 2026-08-22: the scan "seems way too slow", and the proposal is
      that it "should just go in phases so it can easily resume at a phase".

  **Why phases, specifically.** The complaint is duration, but the fix asked for is
  *resumability*, and those are different problems that happen to share a cause. A
  scan that is one long uninterruptible pass has to be re-run from zero after any
  interruption — a deploy, a restart, a crash, a timeout — so its effective cost is
  not its runtime but its runtime times the number of times it gets interrupted.
  Phase checkpoints attack that multiplier without needing any single phase to get
  faster. Note this also removes the "never deploy mid-scan" constraint that
  currently gates every production restart (`docs/operations/`, and the handoff
  runbook), which is a second, separate win.

  **Measure before designing.** Do not assume the slow part. `scheduled.library_scan`
  runs every 360 min, so there is real production timing to pull from
  `journalctl -u audiobook-organizer` rather than guess. The phase boundaries are
  only useful if they fall where the time actually goes, and a phase split chosen
  from intuition will checkpoint in the wrong places.

  **Design notes / open questions:**
  - What are the phases? Candidate split: discover files → parse/probe metadata →
    resolve contributors → write/index → post-scan maintenance. Confirm against
    measured timings, not this list.
  - Where does phase state live, and what makes a phase idempotent enough to resume
    into rather than restart? A phase that is resumable only from its start is still
    a large win over a scan that is resumable only from its start.
  - Interaction with the existing checkpoint machinery — `internal/plugins/maintenance/`
    already has a `pipeline_checkpoint.go` with `checkpointPrefix`/`checkpointTTLDays`
    consts currently flagged as **unused**. Check whether that is a half-built version
    of this idea before writing a second one.
  - Resume must never re-apply metadata or re-write tags for work a prior phase
    already committed; the apply pipeline has a history of double-writing
    (`dedupe-book-file-rows`, the 42-rows/21-paths incident).

  **Not scoped here:** making any individual phase faster. That is a separate
  optimization task and should be filed from the measurements above.
