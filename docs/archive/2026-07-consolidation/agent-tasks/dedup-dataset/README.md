&lt;!-- file: docs/agent-tasks/dedup-dataset/README.md --&gt;
&lt;!-- version: 1.0.0 --&gt;
&lt;!-- guid: 7d0ca852-b1af-41e8-9e70-68115445c94f --&gt;
&lt;!-- last-edited: 2026-07-01 --&gt;

# Workstream — Dedup labeled-dataset follow-ups

Follow-ups on the dedup labeled-training dataset: complete the relation
classifiers, wire live-capture, add export. From C5, C5-sig, C5-folder, C7, C8.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | C5-sig | Offset/subsequence containment in signatureRelation | P2 | M | Sonnet | 1 |
| TASK-02 | C5-folder | sibling_parts folder relation in folderRelation | P2 | S | Haiku | 2 |
| TASK-03 | C5 | Wire BuildExample + Classify into the candidate-upsert path | P2 | M | Sonnet | 3 |
| TASK-04 | C7 | JSONL export endpoint/CLI for labeled examples | P3 | S | Haiku | 1 |
| TASK-05 | C8 | Auto-file a GitHub issue per not_dup cluster after backfill | P3 | M | Sonnet | deferred |

## Ground rules

- Language: Go.
- Build + test after every change:
  ```bash
  go build ./...
  go test ./internal/dedup/... ./internal/database/... -count=1
  ```
- Verify every file:line anchor in a task brief with `grep -n` before editing —
  line numbers drift as other work lands on `main`.

## Collision / wave note

TASK-01 and TASK-02 both edit `internal/dedup/dataset/builder.go` (different
functions, `signatureRelation` vs `folderRelation`) but run in **different
waves** to keep rebases small. TASK-03 edits `internal/dedup/engine.go` and
`internal/database/embedding_store.go` — if the separate dedup-hardening
workstream is touching `engine.go` concurrently, serialize TASK-03 **after**
those tasks land (rebase onto `origin/main` right before starting and right
before opening the PR). TASK-05 is explicitly deferred until the prod
labeled-dataset backfill (Bucket 3 / CONS-10 drain) completes — do not
dispatch it automatically.

See ORCHESTRATION.md (one level up) for the coordinator + worker protocol.
