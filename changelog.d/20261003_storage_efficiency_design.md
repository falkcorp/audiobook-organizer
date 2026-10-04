### Documentation

#### Storage efficiency redesign: approved spec, plan and release A task briefs

Adds the design spec (`docs/design/2026-10-03-storage-efficiency-design.md`)
and implementation plan (`docs/plans/2026-10-03-storage-efficiency-plan.md`)
for storing changes instead of full copies: book history as change entries,
fingerprints and transcripts in a separate signal store, op-log packing and
retention, timeline indexes, and a startup migration with a self-taken
checkpoint. The spec went through three design judges, an eight-lens red-team
workflow (45 confirmed findings applied, 2 refuted) and a final critic. The
owner approved it on 2026-10-03 with decisions Q1-Q9 recorded in section 12.
Release A task briefs (A1-A9) are in `docs/plans/storage-efficiency/`.
