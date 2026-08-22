import json

BASE = "/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/scout-all/"

def load(fn):
    return json.load(open(BASE+fn))

def save(fn, data):
    with open(BASE+fn, 'w') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write("\n")

changes = 0

# TASK-001: scope-03.json title match, reuse[1].verify_grep
data = load('scope-03.json')
obj = next(o for o in data if o.get('title') == "Add a short-TTL cache to the search branch of GetAudiobooksWithTotal (explicit first-cut, defer full dirty-set wiring)")
obj['reuse'][1]['verify_grep'] = "grep -n '\"all:\"' internal/audiobooks/service_query.go"
changes += 1

# TASK-023: scope-03.json
obj = next(o for o in data if o.get('title') == "Investigate then evict/dirty-flag merged-away book/file IDs from every read cache so losers stop appearing after a merge")
obj['verified_anchors'][1] = {
    "claim": "UpsertBookToMemDB/DeleteBookFromMemDB write through memdb (InvalidateLibraryStats itself now lives on PebbleStore in pebble_store.go, not here)",
    "grep_cmd": "grep -n 'func (p \\*PebbleStore) UpsertBookToMemDB\\|func (p \\*PebbleStore) DeleteBookFromMemDB' internal/database/memdb_sync.go",
    "expect": "2 hits: L123 (UpsertBookToMemDB), L195 (DeleteBookFromMemDB)"
}
obj['verified_anchors'].insert(2, {
    "claim": "InvalidateLibraryStats is defined on PebbleStore in pebble_store.go",
    "grep_cmd": "grep -n 'func.*InvalidateLibraryStats' internal/database/pebble_store.go",
    "expect": "1 hit at L237"
})
changes += 1

# TASK-164: scope-03.json, reuse[1].verify_grep
obj = next(o for o in data if o.get('title') == "Let the owner combine/merge duplicate books from the metadata chooser, before applying metadata — BLOCKED on two data-correctness bugs landing first")
obj['reuse'][1]['verify_grep'] = "grep -n 'handleMergeAsVersions' web/src/pages/Library.tsx"
save('scope-03.json', data)

# TASK-006: scope-01.json
data = load('scope-01.json')
obj = next(o for o in data if o.get('title') == "Add a scheduled detect-only backstop workflow for auto-revert.yml")
obj['verified_anchors'][1] = {
    "claim": "the existing issue-filing step creates issues with no pre-check",
    "grep_cmd": "grep -n 'gh issue create' .github/workflows/auto-revert.yml",
    "expect": "1 hit at L305"
}
obj['verified_anchors'].insert(2, {
    "claim": "no pre-check gh issue list/search exists before filing (the dedupe gap)",
    "grep_cmd": "grep -n 'gh issue list' .github/workflows/auto-revert.yml",
    "expect": "0 hits — no pre-check list/search exists before the gh issue create call"
})
changes += 1

# TASK-145: scope-01.json, reuse[0]
obj = next(o for o in data if o.get('title') == "N-6: log + metric when listening-stats read fails (currently silent 0)")
obj['reuse'][0]['file'] = "internal/metrics/metrics.go"
obj['reuse'][0]['verify_grep'] = "grep -n 'prometheus.NewCounterVec\\|promauto' internal/metrics/metrics.go"
obj['reuse'][0]['name'] = "an existing Prometheus counter pattern in internal/metrics/metrics.go (not internal/server — metrics live in their own package)"
save('scope-01.json', data)
changes += 1

# TASK-013: scope-09.json
data = load('scope-09.json')
obj = next(o for o in data if o.get('title') == "Build a report-only scan for book rows that may have been spuriously created by the .tmp-rename bug")
obj['verified_anchors'][0] = {
    "claim": "no existing tooling covers 'purely numeric title' detection by that phrase",
    "grep_cmd": "grep -rln \"purely numeric title\" --include=\"*.py\" --include=\"*.go\" scripts/ internal/",
    "expect": "0 hits"
}
obj['verified_anchors'].insert(1, {
    "claim": "'numeric_title' only appears as an unrelated test-case label (stop-word extraction test), not as spurious-row detection tooling",
    "grep_cmd": "grep -rln \"numeric_title\" --include=\"*.py\" --include=\"*.go\" scripts/ internal/",
    "expect": "1 hit — internal/metafetch/service_test.go:161, a subtest name for stop-word extraction (\"14\" → {\"14\": true}), unrelated to this item's spurious-book-row concept"
})
obj['verified_anchors'].insert(2, {
    "claim": "no existing tooling covers 'spurious...book...row' detection by that phrase",
    "grep_cmd": "grep -rln \"spurious.*book.*row\" --include=\"*.py\" --include=\"*.go\" scripts/ internal/",
    "expect": "0 hits"
})
changes += 1

# TASK-031: scope-09.json
obj = next(o for o in data if o.get('title') == "Lock the three bare globalStore accesses in InitializeStore/CloseStore")
obj['verified_anchors'][4] = {
    "claim": "no production (non-test, non-comment, non-definition) caller of GetGlobalStore() exists today",
    "grep_cmd": "grep -rn \"GetGlobalStore()\" --include=\"*.go\" . | grep -v _test.go | grep -v '//' | grep -v 'func GetGlobalStore'",
    "expect": "0 hits (excluding the func GetGlobalStore() definition itself, which also matches the bare literal text)"
}
changes += 1

# TASK-118: scope-09.json
obj = next(o for o in data if o.get('title') == "Delete internal/operations/mocks — its only referencer is dead, permanently-untagged, currently-broken test code")
obj['verified_anchors'][2] = {
    "claim": "the importer is gated behind a build tag",
    "grep_cmd": "grep -n \"go:build mocks\" internal/server/server_import_file_mocks_test.go",
    "expect": "1 hit at L6"
}
obj['verified_anchors'].insert(3, {
    "claim": "that build tag is never referenced in Makefile or CI workflows",
    "grep_cmd": "grep -rn \"tags mocks\\|tags=mocks\\|build mocks\" Makefile .github/workflows/*.yml",
    "expect": "0 hits — the mocks build tag is never invoked by Makefile or CI"
})
# original index 3 (go vet) is now index 4
obj['verified_anchors'][4] = {
    "claim": "the file is currently broken even under its own tag",
    "grep_cmd": "go vet -tags mocks ./internal/server/... 2>&1 | grep -n 'undefined: queuemocks.NewMockQueue'",
    "expect": "1 hit — vet: internal/server/server_import_file_mocks_test.go:98:26: undefined: queuemocks.NewMockQueue"
}
changes += 1

# TASK-124: scope-09.json
obj = next(o for o in data if o.get('title') == "Reuse internal/ai's existing typed OpenAI error classification in scanner.isPermanentAIFailure instead of re-parsing error text")
obj['verified_anchors'][5] = {
    "claim": "the fragment this comment points at no longer exists in todo.d/",
    "grep_cmd": "find todo.d -iname \"*typed-ai-provider*\"",
    "expect": "0 hits in todo.d/"
}
obj['verified_anchors'].append({
    "claim": "the fragment was already folded into TODO.md at this exact item",
    "grep_cmd": "grep -n \"typed provider errors\" TODO.md",
    "expect": "1 hit in TODO.md at L4852"
})
save('scope-09.json', data)
changes += 1

# TASK-026: scope-04.json
data = load('scope-04.json')
obj = next(o for o in data if o.get('title') == "Triage the remaining misc CodeQL alerts: JS findings, uncontrolled-allocation-size FP, and the drifted clear-text-logging FP")
obj['verified_anchors'][0] = {
    "claim": "uncontrolled-allocation-size is clamped (cap0 <= 4096)",
    "grep_cmd": "grep -n 'cap0 > 4096' internal/database/memdb_summaries.go",
    "expect": "1 hit at L151"
}
obj['verified_anchors'].insert(1, {
    "claim": "no CodeQL suppression comment (lgtm/nosec/nolint) exists near the allocation",
    "grep_cmd": "grep -n 'lgtm\\|nosec\\|nolint' internal/database/memdb_summaries.go",
    "expect": "0 hits — no suppression comment anywhere in the file"
})
changes += 1

# TASK-130: scope-04.json
obj = next(o for o in data if o.get('title') == "Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Prometheus metrics")
obj['verified_anchors'][0] = {
    "claim": "the getter exists in search_reconciler.go",
    "grep_cmd": "grep -n 'func SearchIndexDroppedCount' internal/server/search_reconciler.go",
    "expect": "1 hit at L86"
}
obj['verified_anchors'].append({
    "claim": "nothing registers it as a Prometheus metric in internal/metrics/",
    "grep_cmd": "grep -rn 'SearchIndexDropped' internal/metrics/*.go",
    "expect": "0 hits in internal/metrics/"
})
changes += 1

# TASK-169: scope-04.json, reuse[0]
obj = next(o for o in data if o.get('title') == "Link version_group_id to a filtered library view (now unblocked — the filter works as of commit b0ebccb0)")
obj['reuse'][0]['file'] = "web/src/components/bookdetail/BookDetailHeader.tsx"
obj['reuse'][0]['verify_grep'] = "grep -n 'version_group_id\\|VersionGroupID' web/src/components/bookdetail/BookDetailHeader.tsx"
obj['reuse'][0]['name'] = "BookDetailHeader.tsx (existing component that already reads book.version_group_id — BookDetailVersionGroup.tsx, by contrast, only receives a pre-grouped Book[] and never references the field name directly)"
save('scope-04.json', data)
changes += 1

# TASK-050: scope-15.json
data = load('scope-15.json')
obj = next(o for o in data if o.get('title') == "Shattered-book reassembly: match fragment file-sets against the reference corpus via fpidx containment")
obj['verified_anchors'][1] = {
    "claim": "fpidx LSH probe API exists to build the containment match on (the real symbol is LSHProbe, not Lookup/Query)",
    "grep_cmd": "grep -n 'func.*LSHProbe\\|func.*Subprints' internal/database/pebble_store_lsh.go internal/fingerprint/lsh.go",
    "expect": "2 hits: internal/database/pebble_store_lsh.go:123 (LSHProbe), internal/fingerprint/lsh.go:64 (Subprints)"
}
save('scope-15.json', data)
changes += 1

# TASK-057: scope-13.json
data = load('scope-13.json')
obj = next(o for o in data if o.get('title') == "Phase 8 — write the ABS topology, runbook, and migration guide (Cloudflare Access ordering, cover/image bypass, client compat matrix)")
obj['verified_anchors'][0] = {
    "claim": "no topology/runbook doc scoped to ABS-sync exists today (generic runbook/topology docs do exist elsewhere, e.g. docs/system/runbooks.md, but none is ABS-specific)",
    "grep_cmd": "find docs \\( -iname '*runbook*' -o -iname '*topology*' \\) | grep -i abs",
    "expect": "0 hits — no existing runbook/topology doc is ABS-scoped"
}
save('scope-13.json', data)
changes += 1

# TASK-068: scope-02.json
data = load('scope-02.json')
obj = next(o for o in data if o.get('title') == "Build a REPORT-ONLY counter for Book.FilePath collisions (rows sharing the same path across different books)")
obj['verified_anchors'][0] = {
    "claim": "no existing FilePath-collision counter exists anywhere in the codebase (a loose 'FilePath.*collision|duplicate.*FilePath' regex false-positives on unrelated duplicate-detection code in organizer.go, scanner.go, reconcile.go, and missing_file_repoint.go's unrelated 'collision' repoint bucket)",
    "grep_cmd": "grep -rn 'FilePathCollision\\|CollisionCount\\|filepath_collision' --include=\"*.go\" .",
    "expect": "0 hits"
}
changes += 1

# TASK-069: scope-02.json, reuse[1]
obj = next(o for o in data if o.get('title') == "Give maintenance jobs (v1, internal/maintenance) per-job store interfaces instead of the shared JobStore")
obj['reuse'][1]['verify_grep'] = "grep -n 'maintenance.JobStore.*187 methods' docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md"
save('scope-02.json', data)
changes += 1

# TASK-071: scope-05.json
data = load('scope-05.json')
obj = next(o for o in data if o.get('title') == "Build a detection-only report of other title-fragment author rows (the 57 rows beginning with '-')")
obj['verified_anchors'][0] = {
    "claim": "no existing op reports on title-fragment author names (a loose 'title.fragment|titleFragment' regex false-positives on unrelated prose in author_conjunction_repair.go/_test.go describing a different feature's exclusion rule)",
    "grep_cmd": "grep -rn 'TitleFragmentAuthor\\|title-fragment-author\\|author.title.fragment.report' internal/plugins/maintenance/*.go",
    "expect": "0 hits"
}
save('scope-05.json', data)
changes += 1

# TASK-072: scope-06.json
data = load('scope-06.json')
obj = next(o for o in data if o.get('title') == "New maintenance op: merge an operator-confirmed list of duplicate real-author rows")
obj['verified_anchors'][2] = {
    "claim": "no existing op already merges general name-duplicate authors (maintenance.author-dedup-scan exists but only refreshes the duplicate-group cache; it does not merge)",
    "grep_cmd": "grep -rn 'author-duplicate-merge\\|author-merge\\|merge-author\\|MergeAuthors\\b' internal/plugins/maintenance",
    "expect": "0 hits"
}
save('scope-06.json', data)
changes += 1

# TASK-090, TASK-096: scope-11.json
data = load('scope-11.json')
obj = next(o for o in data if o.get('title') == "Give Change Log row 'Compare snapshot' keyboard/a11y affordance")
obj['verified_anchors'][1]['expect'] = "0 hits — role=\"button\" does not appear anywhere in the file today (grep -c prints \"0\" and exits 1 on no match)"
changes += 1

obj = next(o for o in data if o.get('title') == "Require every mutating operation to declare and enforce dry_run support at the registry")
obj['verified_anchors'][0]['expect'] = "0 hits — OperationDef has no DryRun field today (grep -c prints \"0\" and exits 1 on no match)"
save('scope-11.json', data)
changes += 2

# TASK-106, TASK-108, TASK-113, TASK-114: scope-12.json
data = load('scope-12.json')
obj = next(o for o in data if o.get('title') == "Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan, resolving entries to book_file rows")
obj['verified_anchors'][0] = {
    "claim": "parseM3UFile already exists but only GROUPS files within a single book (the .m3u/.m3u8/.cue scanner case branches), not to create UserPlaylist rows",
    "grep_cmd": "grep -n 'func parseM3UFile' internal/scanner/scanner.go",
    "expect": "1 hit at L1742 — used by the case \".m3u\", \".m3u8\": branch (~L1824) purely for file-grouping, not playlist import"
}
obj['verified_anchors'].insert(1, {
    "claim": "no .pls/.xspf parsing exists at all",
    "grep_cmd": "grep -n '\\.pls\\|\\.xspf' internal/scanner/scanner.go",
    "expect": "0 hits — ParsePLS/ParseXSPF support does not exist yet"
})
changes += 1

obj = next(o for o in data if o.get('title') == "Add the review/rating half of app-to-server reading-state sync (reading status half already exists)")
obj['verified_anchors'][0] = {
    "claim": "no rating/review handling exists in progress.go (whole-word match — case-insensitive substring match false-positives on 'enumerating', which contains 'rating')",
    "grep_cmd": "grep -inw 'rating\\|review' internal/server/handlers/abs/progress.go",
    "expect": "0 hits"
}
changes += 1

obj = next(o for o in data if o.get('title') == "Missing-input triggering: enqueue the producer op when a waiting_deps requirement's input has never run")
obj['verified_anchors'][1] = {
    "claim": "the scheduler has PromoteToQueued",
    "grep_cmd": "grep -n 'PromoteToQueued' internal/operations/registry/deps_scheduler.go",
    "expect": "4 hits (interface decl, comment, call site) in deps_scheduler.go"
}
obj['verified_anchors'].insert(2, {
    "claim": "the scheduler never enqueues a missing producer (no Enqueue-producer function in this file)",
    "grep_cmd": "grep -n 'func.*Enqueue' internal/operations/registry/deps_scheduler.go",
    "expect": "0 hits — no Enqueue-producer function exists in deps_scheduler.go today"
})
changes += 1

obj = next(o for o in data if o.get('title') == "Never delete — re-associate: combine debris books into a template match by duration, then version-group")
obj['verified_anchors'][0] = {
    "claim": "no combine-by-template / Successors-class code exists yet (a loose 'combine.into.one' regex false-positives on the unrelated anthology-detection action-label text in fs_regroup_shape.go/_test.go and regroup_shattered_ai_test.go)",
    "grep_cmd": "grep -rln 'Successors\\|combine_by_template\\|CombineByTemplate' internal --include='*.go'",
    "expect": "0 hits"
}
save('scope-12.json', data)
changes += 1

# TASK-141: scope-14.json
data = load('scope-14.json')
obj = next(o for o in data if o.get('title') == "Add regression tests for the 2 untested deluge hydrate sites")
obj['verified_anchors'][1]['expect'] = "0 hits — bulk_deluge_import_test.go does not exist (ls exits 1 with \"No such file or directory\")"
save('scope-14.json', data)
changes += 1

print("total logical fixes applied:", changes)
