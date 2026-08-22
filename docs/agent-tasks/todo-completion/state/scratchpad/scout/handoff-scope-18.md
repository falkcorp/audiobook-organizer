# scope-18 handoff — COMPLETE

`scout/scope-18.json` is valid JSON, 29 objects, covering all 24 distinct todo_line values from scope-18.md (some split into parts). Verified with `python3 -m json.load`.

## Verdict counts
- actionable: 16
- prod_run: 5
- needs_design: 4
- not_a_task: 2
- stale_done: 1
- parked: 1

## All 24 todo_line items — DONE
280, 283, 476, 697, 928, 3718, 3729, 4064, 4081, 6394, 7209, 7819, 8316 (3 parts), 10077, 10088, 10094, 10104, 10447, 10512 (3 parts), 10600 (2 parts), 10622, 2125, 2267, 2467

## REMAINING: 0

## Notable flags for the coordinator
- **3718** (show_quarantined narrows to is_primary_version=true): root cause NOT located despite reading service_query.go, service_filtering.go, and both memdb_summaries.go index-selection switches in full. Opus-tier bisection needed; candidates ruled out are documented in the object's verdict_evidence/background, next places to look (library_list_warmer.go cache-key collision, Bleve path) are in goal/steps.
- **10077** (TODO-SEC-BIND) and **10094** (TODO-SEC-SYSTEMD) both touch the same two duplicate unit files (deploy/audiobook-organizer.service, deploy/systemd/audiobook-organizer.service, confirmed byte-identical) — coordinate so they don't land as conflicting parallel edits.
- **10104** and **10600 part 1** describe the identical underlying task (TODO-SRVTIMEOUT / newTestServer migration) at two different TODO.md locations — implement once, close both checkboxes.
- **8316 part 3** (wire per-file intro into First Aid): could NOT confirm the exact target module within budget — flagged missing_file_audit.go as closest candidate only, not confirmed. First step must be resolving the `[[first-aid-library-validate-repair]]` cross-reference before implementing.
- **2467** and **7209**: both needs_design, decisions not in the owner's already-made list — flagged with the exact question to ask.
- **928**: not_a_task (pure rationale prose) but flags a real, unassigned actionable item (bump 8 stale github-common workflow pins) that has no todo_line of its own in this scope — coordinator may want to pick it up separately.

## Process notes for future scout runs
- `git show 46628240:TODO.md | sed -n 'START,ENDp'` was used throughout to get baseline context beyond the scope file's excerpt — worked well for 928, 3718/3729, 7819, 7209, 8316.
- Watch for literal tab characters copied from `go test -v` output examples into JSON string values — this broke JSON validity once (fixed) and will recur if citing raw terminal output verbatim in a string field. Always run `python3 -c "import json; json.load(open(path))"` on the final file, not just eyeball it.
- gate template used throughout: `go build ./... && go vet ./... && go test ./<pkg>/... -count=1` for Go; `npm --prefix web run lint && npm --prefix web test -- <file>` for web; `n/a (docs)` / `n/a (prod operation, no code change)` / `n/a (design decision pending)` for non-code verdicts. Never `make ci`.
