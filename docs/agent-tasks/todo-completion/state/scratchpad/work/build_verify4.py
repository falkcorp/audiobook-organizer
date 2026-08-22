import json

OUT = "/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/patches/verify-4.json"

TIER_CMD = "python3 -c \"import json; d=json.load(open('skeleton.json')); [print(t['id'], t['tier'], t['tier_label']) for t in d['tasks'] if t['id'] in ('TASK-057','TASK-142','TASK-150','TASK-157')]\""
TIER_PROBLEM = (
    "Skeleton's machine `tier` field says 'sonnet' but `tier_label` (what the rendered "
    "brief header and README both show) says 'Opus-class', for a REVIEW-CRITICAL prod-data-path "
    "task. Verified via: " + TIER_CMD + " -> all four (057,142,150,157) print 'sonnet Opus-class'. "
    "If the brief is regenerated from the `tier` field (the machine-readable one), a review-critical "
    "prod-data task silently downgrades to Sonnet, violating check 7 (review-critical must be Opus-class, never weak-tier)."
)

def tier_finding():
    return {"severity":"fatal","check":8,"problem":TIER_PROBLEM,"fix":{"tier":"opus"}}

def idem_finding(desc):
    return {"severity":"fatal","check":5,
            "problem": f"Idempotency/Rollback section uses the generic 'the symbol already lives at its NEW location and is absent from the old one' relocation template, but this task is {desc} — there is no 'old location' vs 'new location' to grep for, so a weak model has no concrete, runnable check for whether the task is already applied; it will either invent a nonexistent 'old site' to grep or skip the idempotency check entirely."}

results = []

# TASK-051/052/053 — same-file collision fatal
collision_problem_051 = (
    "docs/README.md's collision section says 'No same-file collisions inside this workstream' and "
    "wave 1 is 'disjoint files within the wave (computed collision matrix)', but TASK-051, TASK-052, "
    "and TASK-053 all list exact_files=['docs/api/openapi.json'] and are all Wave 1 with Depends on: none. "
    "Verified: grep -n 'exact_files' on each task in skeleton.json all resolve to docs/api/openapi.json; "
    "all three briefs' '## ... Wave: 1' headers confirmed via grep -n 'Wave:' on each TASK file. Three weak "
    "models editing the same JSON paths object in parallel worktrees, each deleting a different subset of "
    "path entries with no serialization, will produce conflicting diffs/merge order problems on the same file — "
    "exactly the 'break a sibling' failure mode."
)
for tid, extra in [("TASK-051",""), ("TASK-052",""), ("TASK-053","")]:
    results.append({"task_id":tid,"verdict":"fail","findings":[
        {"severity":"fatal","check":6,"problem":collision_problem_051,
         "fix":{"depends_on_lines":["TASK-051 (wave1) -> TASK-052 (wave2, depends on TASK-051) -> TASK-053 (wave3, depends on TASK-052), all serialized on docs/api/openapi.json"]}}
    ]})

# TASK-054
results.append({"task_id":"TASK-054","verdict":"fail","findings":[
    idem_finding("a re-verification/prose-update of an existing doc section (§11 'safe to stub' list) in place — not a symbol move")
]})

# TASK-055 - pass
results.append({"task_id":"TASK-055","verdict":"pass","findings":[]})

# TASK-056
results.append({"task_id":"TASK-056","verdict":"fail","findings":[
    idem_finding("a consolidation/edit of an existing executive-summary doc in place — not a symbol move")
]})

# TASK-057
results.append({"task_id":"TASK-057","verdict":"fail","findings":[
    tier_finding(),
    {"severity":"fatal","check":4,"problem":(
        "Tests section says 'N/A — documentation only' and Acceptance has zero apply/dry-run/undo checkboxes, "
        "yet Idempotency/Rollback carries the full 'this task touches persisted data... Mandatory: (1) apply=false "
        "dry-run default (2) journaled through CreateOperationChange (3) byte-identical undo-fixture test (4) refuse "
        "while library.scan is running' boilerplate for what is purely writing a new markdown runbook. None of those "
        "4 mandatory items map to any real op/endpoint this task creates, so a weak model either invents a fake "
        "apply/dry-run path that doesn't belong in a docs task, or silently ignores 'Mandatory' language — undefined "
        "which, and neither is checkable by any acceptance command."
    )}
]})

# TASK-058
results.append({"task_id":"TASK-058","verdict":"fail","findings":[
    idem_finding("an update of an existing execution-manifest doc's status table in place — not a symbol move")
]})

# TASK-059
TASK059_PROBLEM_CONTRA = (
    "Step 1 explicitly instructs: 'In TODO.md, replace the L10706-10709 bullet body with a closure note...' "
    "— a direct edit to TODO.md. But the same brief's standard 'Then, always' block says 'Do NOT edit TODO.md "
    "— the coordinator closes the source item in one commit per wave... In your final report, state the exact "
    "TODO.md line text to check off.' These directly contradict: one weak model follows step 1 and edits TODO.md "
    "itself; another follows the boilerplate rule and instead only reports the line text. Verified via reading "
    "the brief's Step 1 and 'Then, always' sections side by side (both present in the same file)."
)
TASK059_PROBLEM_FILES = (
    "exact_files is [] (empty) in skeleton.json for TASK-059, but Step 2 requires creating a new todo.d/ fragment "
    "file for DEP-1e, and Step 1 (per the brief's own text) edits TODO.md directly — neither file is listed in "
    "exact_files. Verified: python3 -c \"import json;d=json.load(open('skeleton.json'));print([t['exact_files'] for t in d['tasks'] if t['id']=='TASK-059'])\" -> []."
)
TASK059_PROBLEM_R9 = (
    "Step 1's closure note claims 'TEST-2/DEAD-1/CTX-4/LOG-5/R-9/R-10/DEP-1a-d all verified resolved or moot', "
    "but the brief's 're-verify these anchors' block (and the skeleton's verified_anchors list, 7 entries) has "
    "zero grep evidence for R-9 — only DEAD-1, CTX-4, LOG-5, R-10, DEP-1a-d, DEP-1e, and TEST-2 have anchors. "
    "R-9 is asserted resolved with no verification at all, unlike every sibling item in the same sentence."
)
TASK059_PROBLEM_GREP = (
    "The DEP-1e re-verify anchor's second grep — `grep -n 'ITunesPath: b.ITunesPath\\|ITunesPath: c.ITunesPath' "
    "internal/database/bookcore.go` — returns 0 hits at HEAD (verified by running it directly), not the 2 hits "
    "the brief's inline comment claims ('1 hit + 2 hits'). The actual source at bookcore.go:207,321 uses gofmt-aligned "
    "multi-space formatting ('ITunesPath:               b.ITunesPath,'), which the single-space grep pattern misses. "
    "A weak model running this exact grep as instructed gets a false negative and would wrongly conclude the field "
    "is not copied at those sites."
)
results.append({"task_id":"TASK-059","verdict":"fail","findings":[
    {"severity":"fatal","check":6,"problem":TASK059_PROBLEM_CONTRA},
    {"severity":"fatal","check":9,"problem":TASK059_PROBLEM_FILES,"fix":{"exact_files":["TODO.md"]}},
    {"severity":"fatal","check":2,"problem":TASK059_PROBLEM_R9},
    {"severity":"fatal","check":2,"problem":TASK059_PROBLEM_GREP,
     "fix":{"verified_anchors":[{"claim":"DEP-1e still open — field still declared and copied","grep_cmd":"grep -n 'ITunesPath: *b.ITunesPath\\|ITunesPath: *c.ITunesPath' internal/database/bookcore.go","expect":"2 hits (L207, L321), tolerant of gofmt alignment spaces"}]}}
]})

# TASK-060
results.append({"task_id":"TASK-060","verdict":"fail","findings":[
    idem_finding("an update of existing docs (STATUS.md / pending-prod-actions.md) with measured numbers in place — not a symbol move")
]})

# TASK-123 - pass
results.append({"task_id":"TASK-123","verdict":"pass","findings":[]})

# TASK-124
results.append({"task_id":"TASK-124","verdict":"fail","findings":[
    idem_finding("an in-place swap of ai_failure.go's string-matching body for the typed internal/ai classifier, in the same file — not a move to a new file/location")
]})

# TASK-125/126 - pass
results.append({"task_id":"TASK-125","verdict":"pass","findings":[]})
results.append({"task_id":"TASK-126","verdict":"pass","findings":[]})

# TASK-142
results.append({"task_id":"TASK-142","verdict":"fail","findings":[
    tier_finding(),
    {"severity":"fatal","check":3,"problem":(
        "Step 3 adds a real guard: ListAutoMergeJournalEntries(0) means UNLIMITED per its doc comment today, but "
        "the new handler must 'supply a real default rather than passing through a raw 0' (cap 50) — this changes "
        "behavior for any future limit=0 caller from 'all entries' to 'first 50'. Both 'Tests' and 'Acceptance' say "
        "'Anti-over-suppression: N/A'. No listed test asserts a large/legit journal (>50 entries, or a caller relying "
        "on limit=0=unlimited) still gets correct results under the new default — the guard is untested for its "
        "happy-path/non-suppression case."
    )}
]})

# TASK-143/144
results.append({"task_id":"TASK-143","verdict":"fail","findings":[idem_finding("flipping two boolean fields in defaultPermissions() in place — not a symbol move")]})
results.append({"task_id":"TASK-144","verdict":"fail","findings":[idem_finding("removing a hardcoded numBooks:0 field from an existing gin.H literal in place — not a symbol move")]})

# TASK-145 - pass (lighter review: anchors clean, idempotency template matches additive polarity)
results.append({"task_id":"TASK-145","verdict":"pass","findings":[]})

# TASK-146/147/148
results.append({"task_id":"TASK-146","verdict":"fail","findings":[idem_finding("replacing a hardcoded literal (RateLimitLoginRequests: 10) with a derived constant expression in the same struct literal — not a symbol move")]})
results.append({"task_id":"TASK-147","verdict":"fail","findings":[idem_finding("editing existing ABS conformance test fixtures/expectations in place to fix 12 red tests — not a symbol move")]})
results.append({"task_id":"TASK-148","verdict":"fail","findings":[idem_finding("re-capturing a test fixture JSON file's contents in place — not a symbol move")]})

# TASK-149 - pass
results.append({"task_id":"TASK-149","verdict":"pass","findings":[]})

# TASK-150
results.append({"task_id":"TASK-150","verdict":"fail","findings":[
    tier_finding(),
    {"severity":"fatal","check":4,"problem":(
        "Goal is a read-only investigation producing docs/audits/2026-08-21-apply-endpoint-fileio-audit.md; "
        "Step 6 explicitly says 'do not fix it inline here' even if a defect is found. Tests says 'N/A for the "
        "audit itself.' Yet Idempotency/Rollback carries the full apply-path boilerplate: 'Mandatory: (1) "
        "apply=false dry-run default (2) CreateOperationChange journaling (3) byte-identical undo-fixture test "
        "(4) refuse while library.scan is running.' None of these map to any code this task writes (it writes one "
        "new markdown file). Not one of these 4 'mandatory' items appears as an actual Acceptance checkbox, so "
        "they're unenforceable as written, and a weak model has no way to satisfy or verify them for a docs-only audit."
    )}
]})

# TASK-151/152
results.append({"task_id":"TASK-151","verdict":"fail","findings":[idem_finding("adding an explanatory comment above an existing hardcoded TimeBase value in place — not a symbol move")]})
results.append({"task_id":"TASK-152","verdict":"fail","findings":[idem_finding("adding a bound/cap to an existing unbounded SearchBooks(search,0,0) call in place — not a symbol move")]})

# TASK-153/154/155/156 - pass (idempotency templates match their polarity; anchors verified clean)
results.append({"task_id":"TASK-153","verdict":"pass","findings":[]})
results.append({"task_id":"TASK-154","verdict":"pass","findings":[]})
results.append({"task_id":"TASK-155","verdict":"pass","findings":[]})
results.append({"task_id":"TASK-156","verdict":"pass","findings":[]})

# TASK-157
results.append({"task_id":"TASK-157","verdict":"fail","findings":[tier_finding()]})

# TASK-182/183 - pass
results.append({"task_id":"TASK-182","verdict":"pass","findings":[]})
results.append({"task_id":"TASK-183","verdict":"pass","findings":[]})

with open(OUT, "w") as f:
    json.dump(results, f, indent=1)

print("wrote", len(results), "entries")
fails = sum(1 for r in results if r["verdict"]=="fail")
passes = sum(1 for r in results if r["verdict"]=="pass")
print("pass:", passes, "fail:", fails)
