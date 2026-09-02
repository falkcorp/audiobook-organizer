#!/usr/bin/env bash
# file: scripts/mutation-matrix.sh
# version: 1.5.0
# guid: 7f3b6d21-4c98-4e07-b153-2a86f0e9c47d
# last-edited: 2026-09-02
#
# Runs a HAND-AUTHORED table of semantic mutations against one package and
# reports, per mutation, whether the test suite caught it.
#
# ── WHY THIS EXISTS ALONGSIDE `make mutate` ───────────────────────────────────
#
# `make mutate` (gremlins, scripts/run-mutation.sh) generates mutants
# AUTOMATICALLY from the syntax tree: flip a conditional boundary, invert a
# negation, swap an arithmetic operator. That is the right tool for breadth, and
# it needs no authoring.
#
# It cannot express the mutation that actually matters most often here, because
# that mutation is SEMANTIC rather than syntactic. The one that motivated this
# script:
#
#     report.SignalsMissing.tally(f)   <->   report.SignalsPresent.tally(f)
#
# Swapping two arms of a census is a perfectly valid program. No operator
# changed, no boundary moved, nothing is syntactically suspicious -- and it
# silently inverts the meaning of every number the audit reports. gremlins will
# never generate it. A human who knows what the code MEANS writes it in one line
# of a table.
#
# So: gremlins for breadth, this for the specific lies you are worried about.
# They answer different questions and both are cheap.
#
# ── THE FIVE GUARDS ───────────────────────────────────────────────────────────
#
# Each covers a way this kind of harness reports a number that is not a
# measurement. All five have burned someone on this repo already.
#
#   1. REFUSE A DIRTY TREE. The harness restores each mutation with
#      `git checkout -- <file>`. That command does not distinguish a mutation
#      from your uncommitted work; it discards both. Commit first, always.
#
#   2. REQUIRE A GREEN BASELINE. If the suite is already red, every mutation is
#      recorded as "killed" without the mutation having anything to do with it,
#      and the score reads 100% while measuring nothing.
#
#   3. VERIFY EACH MUTATION ACTUALLY APPLIED. A perl pattern that matches
#      nothing leaves the file untouched, the suite passes, and the run reports
#      a KILLED-looking clean result for a mutation that never existed. Reported
#      as NOT-APPLIED, which is a broken instrument, not a passing test.
#
#   4. SEPARATE A BUILD FAILURE FROM A KILL. A mutation that does not compile
#      fails `go test` for a reason that has nothing to do with your assertions.
#      Counting it as killed inflates the score. Reported as BUILD-FAIL.
#
#   5. REFUSE A PATTERN THAT MATCHES ANYWHERE. Guard 3 asks "did the file
#      change", not "did the INTENDED text change" -- and those come apart the
#      moment a pattern can match the zero-length string. Then `s///` fires at
#      offset 0, rewrites nothing anyone meant, changes the file just enough to
#      satisfy guard 3, compiles, passes, and is reported SURVIVED. That is a
#      false coverage gap: it sends you writing a test for a branch the harness
#      never touched.
#
#      This is not hypothetical. Until 2026-08-30 this script rewrote the
#      documented `\x7c` escape into a bare `|` before handing the expression to
#      perl (see TABLE FORMAT below). In a perl PATTERN a bare `|` is
#      ALTERNATION, so `A \x7c\x7c B` became `A || B` -- alternation with two
#      EMPTY branches, which matches the empty string at offset 0. M16 of
#      scripts/mutation-tables/activity-index-pushdown.muts, a mutation meant to
#      delete a four-field eligibility gate, instead prepended a tab and a
#      newline to line 1 of the file and was scored SURVIVED for two days.
#
#      The guard runs the expression against a ONE-BYTE sentinel string before
#      touching the source file and requires the output be byte-identical. No
#      real mutation pattern can match one arbitrary byte; a zero-width one
#      matches every input there is. Reported as NOT-APPLIED.
#
#      The sentinel is one byte and not the empty string on purpose: under
#      `-0777 -p` perl's read loop never executes on a zero-length stream, so an
#      empty sentinel produces empty output for a broken pattern and a working
#      one alike, and the check would pass for both.
#
# ── WHY EACH KILL NAMES THE TEST THAT CAUGHT IT ───────────────────────────────
#
# There is a further failure mode no guard can prevent, only make visible: a FLAKY
# suite. Guard 2 checks the baseline once, so a test that fails intermittently
# can score a mutation as KILLED without the mutation having been detected at
# all -- and a bare killed/survived count gives you no way to notice.
#
# The remedy is the third column. Every KILLED line records WHICH test failed,
# so you can check that a mutation was caught by the test you expected rather
# than by unrelated noise. If a mutation to the census is reported killed by
# some distant reconcile test, that is not a kill, that is a flake. Read the
# attribution, not just the score.
#
# ── TABLE FORMAT ──────────────────────────────────────────────────────────────
#
# One mutation per line, three pipe-separated fields; blank lines and lines
# starting with `#` are ignored:
#
#     NAME | FILE | PERL_EXPRESSION
#
# NAME is free text for the report. FILE is repo-relative. PERL_EXPRESSION is
# handed to `perl -0777 -pi -e` so it sees the whole file as one string, which
# is what lets a mutation span lines (dropping a `decisive = true` that follows
# a specific counter, for example). Escape `|` inside an expression as `\x7c`.
#
# THE `\x7c` ESCAPE IS PASSED THROUGH TO PERL VERBATIM. Nothing in this script
# turns it back into a `|` first, and nothing may: perl already reads `\x7c` as
# the literal character `|` in BOTH halves of an s///, matching a literal pipe
# in the pattern and interpolating to one in the replacement. Un-escaping it in
# the shell strips exactly the protection the escape exists to give -- in a
# pattern the result is alternation, not a pipe. See guard 5. The escape is
# needed because the parser splits fields on `|`; write `\x7c` for every pipe
# you mean literally, in the pattern and the replacement alike.
#
# ── USAGE ─────────────────────────────────────────────────────────────────────
#
#     scripts/mutation-matrix.sh --pkg ./internal/plugins/maintenance/ \
#         --table scripts/mutation-tables/missing-file-census.muts \
#         --out /tmp/results.txt
#
# Options:
#     --pkg PKG        Go package to test (required)
#     --table FILE     mutation table (required)
#     --out FILE       write results here (default: stdout only)
#     --run REGEX      pass -run REGEX to go test. Use only when the full package
#                      is too slow to iterate on, and read the results knowing:
#
#                        A MUTATION CAUGHT ONLY BY AN EXCLUDED TEST IS REPORTED
#                        AS SURVIVED. That is a FALSE GAP.
#
#                      It sends you writing an assertion that already exists
#                      somewhere else in the package. This is the mirror image of
#                      the inflation guards 2-4 prevent, and the one thing here
#                      no guard can catch for you -- only the full-package
#                      default is self-verifying. Confirm any survivor found
#                      under --run by re-running that one mutation without it.
#     --fail-on-survivor   exit 1 if any mutation survived, for CI gating
#
# Exits 0 on a completed run, 2 on a guard failure or bad usage, and 1 only with
# --fail-on-survivor when something survived.

set -euo pipefail

PKG=""
TABLE=""
OUT=""
RUN_REGEX=""
FAIL_ON_SURVIVOR=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --pkg) PKG="${2:-}"; shift 2 ;;
        --table) TABLE="${2:-}"; shift 2 ;;
        --out) OUT="${2:-}"; shift 2 ;;
        --run) RUN_REGEX="${2:-}"; shift 2 ;;
        --fail-on-survivor) FAIL_ON_SURVIVOR=1; shift ;;
        # Print the whole comment header rather than a fixed line range: the
        # header grows, and a hardcoded range silently truncates the guidance
        # mid-sentence the first time someone adds a paragraph to it.
        -h|--help) awk '/^set -euo/{exit} NR>1' "$0"; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

[[ -z "$PKG" ]] && { echo "--pkg is required" >&2; exit 2; }
[[ -z "$TABLE" ]] && { echo "--table is required" >&2; exit 2; }
[[ -f "$TABLE" ]] || { echo "table not found: $TABLE" >&2; exit 2; }

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# ── Guard 1: never mutate on top of uncommitted work ──────────────────────────
#
# Collected before anything is touched. The restore path is `git checkout --`,
# which would take your edits with it.
if [[ -n "$(git status --porcelain)" ]]; then
    echo "REFUSING: working tree is dirty." >&2
    echo "  This harness restores each mutation with 'git checkout -- <file>'," >&2
    echo "  which discards uncommitted changes along with the mutation." >&2
    echo "  Commit or stash first." >&2
    exit 2
fi

TMP_LOG="$(mktemp -t mutation-matrix)"
SENTINEL_IN="$(mktemp -t mutation-sentinel-in)"
SENTINEL_OUT="$(mktemp -t mutation-sentinel-out)"
SENTINEL_ERR="$(mktemp -t mutation-sentinel-err)"

# Files this run has touched, used only by the EXIT trap. Deliberately a
# deduplicated set rather than one entry per mutation: a table that mutates the
# same file 22 times would otherwise queue 22 identical `git checkout --` calls
# on the interrupt path, which is the path that most needs to be simple.
MUTATED_FILES=()
track_file() {
    local seen
    for seen in "${MUTATED_FILES[@]:-}"; do
        [[ "$seen" == "$1" ]] && return 0
    done
    MUTATED_FILES+=("$1")
}

restore_all() {
    local f
    for f in "${MUTATED_FILES[@]:-}"; do
        [[ -n "$f" ]] && git checkout -- "$f" 2>/dev/null || true
    done
    rm -f "$TMP_LOG" "$SENTINEL_IN" "$SENTINEL_OUT" "$SENTINEL_ERR"
}
trap restore_all EXIT INT TERM

emit() {
    echo "$1"
    [[ -n "$OUT" ]] && echo "$1" >> "$OUT"
    return 0
}

[[ -n "$OUT" ]] && : > "$OUT"

go_test() {
    if [[ -n "$RUN_REGEX" ]]; then
        go test "$PKG" -run "$RUN_REGEX" -count=1
    else
        go test "$PKG" -count=1
    fi
}

emit "# mutation-matrix | $(git rev-parse --short HEAD) | pkg=$PKG | table=$TABLE"
[[ -n "$RUN_REGEX" ]] && emit "# ⚠️  denominator NARROWED to tests matching: $RUN_REGEX"

# ── Guard 2: a red baseline makes every result meaningless ────────────────────
emit "# baseline: running the suite unmutated..."
if ! go_test > "$TMP_LOG" 2>&1; then
    emit "# BASELINE RED -- refusing to run."
    emit "# Every mutation would be recorded as killed regardless of the mutation,"
    emit "# and the score would read 100% while measuring nothing. Fix the suite first."
    # Name the failing tests rather than dumping a blind tail. These packages log
    # heavily, and a fixed tail buries the one line you need under warmup noise --
    # which forces you to re-run the suite by hand just to learn what a guard
    # already knew. If nothing matched, say so explicitly: a FAIL with no
    # `--- FAIL:` usually means a panic or a build error, not an assertion.
    failed="$(grep -E '^\s*--- FAIL: ' "$TMP_LOG" | sed 's/^[[:space:]]*--- FAIL: //' | sort -u)"
    if [[ -n "$failed" ]]; then
        emit "# failing tests:"
        while IFS= read -r line; do emit "#   $line"; done <<< "$failed"
    else
        emit "# no '--- FAIL:' line found -- likely a panic or build error, not an assertion."
    fi
    # Deliberately NOT deleted: the whole log is the only record of a red baseline,
    # and re-running to recover it is exactly the waste this branch exists to avoid.
    kept="${TMP_LOG}.baseline-fail"
    cp "$TMP_LOG" "$kept" 2>/dev/null && emit "# full output kept at: $kept"
    exit 2
fi
emit "# baseline GREEN"
emit ""

killed=0; survived=0; notapplied=0; buildfail=0; total=0

# `|| [[ -n ... ]]` KEEPS A FINAL LINE THAT HAS NO TRAILING NEWLINE. Without it
# `read` returns non-zero on that line, bash leaves the loop before running the
# body, and the last mutation in the table is never executed -- silently, because
# "mutations attempted" is counted inside the loop and so is short by one too.
# Found on 2026-08-30: activity-index-pushdown.muts ended without a newline, so
# M18 had never once run, and M18 is the entry documented as an EXPECTED
# equivalent survivor. A survivor that is simply ABSENT from the report reads
# exactly like "confirmed survived, do not chase".
# ── Guard 6: process EVERY entry, or say so ───────────────────────────────────
#
# The loop below used to read the table on stdin, which every child process it
# spawned inherited. `go test` reads stdin, so it ATE the remaining table: a
# 5-mutation table silently reported 3 results, no summary, exit 0. Nothing in
# guards 1-5 can see that, because each mutation that DID run ran correctly --
# the lie is in the ones that never ran at all, and a short list looks exactly
# like a short table.
#
# Two changes keep it fixed: the table is now read on fd 3 (children inherit
# stdin, not fd 3), and the expected entry count is computed here and checked
# against the processed count at the end.
expected_entries="$(grep -cE '^[[:space:]]*[^#[:space:]].*\|' "$TABLE" || true)"
: "${expected_entries:=0}"

while IFS='|' read -r name file expr <&3 || [[ -n "${name:-}${file:-}${expr:-}" ]]; do
    name="$(echo "${name:-}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
    file="$(echo "${file:-}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
    # NO `\x7c` -> `|` rewrite here. perl reads `\x7c` as a literal pipe itself,
    # and doing it in the shell first hands a PATTERN an alternation operator.
    # That is guard 5's whole subject; deleting this comment and "simplifying"
    # the escape away reintroduces a two-day-old silent false SURVIVED.
    expr="$(echo "${expr:-}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"

    [[ -z "$name" || "${name:0:1}" == "#" ]] && continue
    if [[ -z "$file" || -z "$expr" ]]; then
        emit "MALFORMED  | $name | needs NAME|FILE|EXPR"
        continue
    fi
    if [[ ! -f "$file" ]]; then
        emit "MALFORMED  | $name | no such file: $file"
        continue
    fi

    total=$((total + 1))

    # ── Guard 5: a pattern that can match anywhere is not a mutation ───────────
    #
    # Run the expression against one arbitrary byte. A real mutation pattern
    # quotes source text and cannot match it; a zero-width pattern -- the shape
    # a stray `|` in a pattern produces -- matches it, and would go on to match
    # at offset 0 of the source file, leaving the intended text untouched while
    # changing the file enough to satisfy guard 3. Checked BEFORE the source file
    # is touched, so a rejected entry never needs restoring.
    printf 'x' > "$SENTINEL_IN"
    if ! perl -0777 -pe "$expr" < "$SENTINEL_IN" > "$SENTINEL_OUT" 2>"$SENTINEL_ERR"; then
        emit "NOT-APPLIED| $name | expression failed to run: $(head -1 "$SENTINEL_ERR") -- FIX THE TABLE"
        notapplied=$((notapplied + 1))
        continue
    fi
    if ! cmp -s "$SENTINEL_IN" "$SENTINEL_OUT"; then
        emit "NOT-APPLIED| $name | pattern matches a 1-byte sentinel, so it matches ANY input and would fire at offset 0 instead of on your target -- an unescaped '|' in a pattern is alternation; write it as \\x7c. FIX THE TABLE"
        notapplied=$((notapplied + 1))
        continue
    fi

    track_file "$file"

    perl -0777 -pi -e "$expr" "$file"

    # ── Guard 3: a pattern that matched nothing is a broken instrument ─────────
    if git diff --quiet -- "$file"; then
        emit "NOT-APPLIED| $name | pattern matched nothing -- FIX THE TABLE, this is not a result"
        notapplied=$((notapplied + 1))
        git checkout -- "$file"
        continue
    fi

    if go_test > "$TMP_LOG" 2>&1; then
        emit "SURVIVED   | $name"
        survived=$((survived + 1))
    else
        # ── Guard 4: not compiling is not the same as being caught ────────────
        if grep -qE 'build failed|cannot use|undefined:|declared and not used|syntax error' "$TMP_LOG"; then
            emit "BUILD-FAIL | $name | mutation does not compile -- not a kill, rewrite it"
            buildfail=$((buildfail + 1))
        else
            by="$(grep -oE '\-\-\- FAIL: [A-Za-z_0-9/]+' "$TMP_LOG" \
                  | sed 's/--- FAIL: //' | sort -u | head -3 | paste -sd, -)"
            emit "KILLED     | $name | ${by:-unknown test}"
            killed=$((killed + 1))
        fi
    fi

    git checkout -- "$file"
done 3< "$TABLE"

if [[ "$total" -ne "$expected_entries" ]]; then
    emit "TRUNCATED  | processed $total of $expected_entries table entries -- this run is NOT a measurement. See guard 6."
    exit 1
fi

scored=$((killed + survived))
emit ""
emit "# ── summary ──"
emit "# mutations attempted : $total"
emit "# killed              : $killed"
emit "# SURVIVED            : $survived   <- each is a gap in the tests"
emit "# not applied         : $notapplied   <- broken table entries, not results"
emit "# build failures      : $buildfail   <- invalid mutations, not results"
if [[ "$scored" -gt 0 ]]; then
    emit "# score               : $killed/$scored killed ($(( 100 * killed / scored ))%)"
    emit "#   Denominator is killed+survived. Not-applied and build-fail entries are"
    emit "#   EXCLUDED deliberately: counting them would let a broken table inflate"
    emit "#   the score, which is the exact failure this harness exists to prevent."
else
    emit "# score               : n/a -- nothing scoreable ran"
fi

# A terminal marker. Results are emitted incrementally, so a run killed partway
# through leaves a file that looks exactly like a completed one minus the summary
# -- and a reader who skims to the last KILLED line has no way to tell. If this
# marker is absent, the run did NOT finish and the counts below it are partial.
#
# This is not hypothetical: the first real use of this script was truncated
# because the script FILE was edited while running. bash reads a script
# incrementally by byte offset, so editing it in place shifts the ground under
# the interpreter and it resumes mid-construct. Do not edit a running shell
# script; and when a results file has no END marker, do not trust its totals.
emit "# END OF RUN"

if [[ "$FAIL_ON_SURVIVOR" -eq 1 && "$survived" -gt 0 ]]; then
    exit 1
fi
exit 0
