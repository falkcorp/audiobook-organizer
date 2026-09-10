import json, subprocess

BASE = "/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/scout-all/"
REPO = "/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer"

targets = {
 "scope-03.json": ["Add a short-TTL cache to the search branch of GetAudiobooksWithTotal (explicit first-cut, defer full dirty-set wiring)",
                    "Investigate then evict/dirty-flag merged-away book/file IDs from every read cache so losers stop appearing after a merge",
                    "Let the owner combine/merge duplicate books from the metadata chooser, before applying metadata — BLOCKED on two data-correctness bugs landing first"],
 "scope-01.json": ["Add a scheduled detect-only backstop workflow for auto-revert.yml",
                    "N-6: log + metric when listening-stats read fails (currently silent 0)"],
 "scope-09.json": ["Build a report-only scan for book rows that may have been spuriously created by the .tmp-rename bug",
                    "Lock the three bare globalStore accesses in InitializeStore/CloseStore",
                    "Delete internal/operations/mocks — its only referencer is dead, permanently-untagged, currently-broken test code",
                    "Reuse internal/ai's existing typed OpenAI error classification in scanner.isPermanentAIFailure instead of re-parsing error text"],
 "scope-04.json": ["Triage the remaining misc CodeQL alerts: JS findings, uncontrolled-allocation-size FP, and the drifted clear-text-logging FP",
                    "Register SearchIndexDroppedCount (and a dirty-backlog gauge) as Prometheus metrics",
                    "Link version_group_id to a filtered library view (now unblocked — the filter works as of commit b0ebccb0)"],
 "scope-15.json": ["Shattered-book reassembly: match fragment file-sets against the reference corpus via fpidx containment"],
 "scope-13.json": ["Phase 8 — write the ABS topology, runbook, and migration guide (Cloudflare Access ordering, cover/image bypass, client compat matrix)"],
 "scope-02.json": ["Build a REPORT-ONLY counter for Book.FilePath collisions (rows sharing the same path across different books)",
                    "Give maintenance jobs (v1, internal/maintenance) per-job store interfaces instead of the shared JobStore"],
 "scope-05.json": ["Build a detection-only report of other title-fragment author rows (the 57 rows beginning with '-')"],
 "scope-06.json": ["New maintenance op: merge an operator-confirmed list of duplicate real-author rows"],
 "scope-11.json": ["Give Change Log row 'Compare snapshot' keyboard/a11y affordance",
                    "Require every mutating operation to declare and enforce dry_run support at the registry"],
 "scope-12.json": ["Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan, resolving entries to book_file rows",
                    "Add the review/rating half of app-to-server reading-state sync (reading status half already exists)",
                    "Missing-input triggering: enqueue the producer op when a waiting_deps requirement's input has never run",
                    "Never delete — re-associate: combine debris books into a template match by duration, then version-group"],
 "scope-14.json": ["Add regression tests for the 2 untested deluge hydrate sites"],
}

def run(cmd):
    r = subprocess.run(["bash","-c",cmd], cwd=REPO, capture_output=True, text=True)
    return r.returncode, r.stdout.strip()

fail_count = 0
for fn, titles in targets.items():
    data = json.load(open(BASE+fn))
    for t in titles:
        obj = next(o for o in data if o.get('title')==t)
        for a in obj.get('verified_anchors', []):
            cmd, exp = a['grep_cmd'], a['expect']
            rc, out = run(cmd)
            nhits = len(out.splitlines()) if out else 0
            expects_zero = exp.strip().lower().startswith("0 hit")
            if expects_zero and nhits != 0:
                print(f"MISMATCH [{fn}] {t[:40]!r} :: {cmd!r} -> {nhits} hits but expect zero")
                fail_count += 1
            elif (not expects_zero) and nhits == 0:
                print(f"MISMATCH [{fn}] {t[:40]!r} :: {cmd!r} -> 0 hits but expect non-zero: {exp!r}")
                fail_count += 1
        for rentry in obj.get('reuse', []):
            cmd = rentry.get('verify_grep')
            if not cmd: continue
            rc, out = run(cmd)
            nhits = len(out.splitlines()) if out else 0
            if nhits == 0:
                print(f"REUSE ZERO [{fn}] {t[:40]!r} :: {cmd!r} -> 0 hits")
                fail_count += 1
print("done, mismatches:", fail_count)
