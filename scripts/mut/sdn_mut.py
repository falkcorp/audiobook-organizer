# Throwaway mutation driver for the series-denumber safe-apply PR. Not for merge.
# Prints "MUTANT <name>: KILLED|SURVIVED ..." per mutant, after an unmutated
# baseline that must be green (and must have run a plausible number of tests).
import subprocess, sys

M = "internal/plugins/maintenance/"
OP = M + "series_denumber_op.go"
PL = M + "series_denumber.go"
T = "\t"
PATTERN = "SeriesDenumber|ApplyEligible|SplitSeriesPosition|JunkSeriesBase|DenumberReport|Demote"

# (name, file, old, new)
MUTANTS = [
    ("B-select-counts-every-plan", OP, "Books: len(run.results[i].Moves)}", "Books: 1}"),
    ("B-select-counts-held-too", OP, "Books: len(run.results[i].Moves)}", "Books: len(run.members[pl.FromID])}"),
    ("B-base-appeared-not-skipped", OP, "case existing != nil:", "case false:"),
    ("U-base-create-not-journaled", OP, "jerr = record(&database.OperationChange{SeriesID: &nsID, ChangeType: undo.ChangeTypeSeriesCreate, NewValue: snap})", "jerr = nil"),
    ("U-delete-not-journaled", OP, "jerr = record(&database.OperationChange{SeriesID: &srcID, ChangeType: undo.ChangeTypeSeriesRowDelete, OldValue: snap})", "jerr = nil"),
    ("U-move-rows-not-journaled", OP, "for _, row := range sdn.MoveRows(cur, targetID) {", "for _, row := range []sdn.Row{} {"),
    ("U-journal-failure-ignored", OP, "}); err != nil {\n" + T*6 + "return err", "}); err != nil {\n" + T*6 + "_ = err"),
    ("C-fresh-source-recheck-off", OP, "if full.SeriesID == nil || *full.SeriesID != pl.FromID {", "if false {"),
    ("C-fresh-position-recheck-off", OP, "if full.SeriesSequence != nil && *full.SeriesSequence != mv.NewSeq {", "if false {"),
    ("C-set-seq-always", OP, "cur.SetSeq = full.SeriesSequence == nil", "cur.SetSeq = true"),
    ("C-chained-target-unwired", OP, "TargetIsSource: g.targetID != 0 && sourceIDs[g.targetID]", "TargetIsSource: false"),
    ("C-groups-ignore-author", OP, 'nameKey := fmt.Sprintf("%s\\x00%d", strings.ToLower(pl.IntoName), pl.AuthorID)', "nameKey := strings.ToLower(pl.IntoName)"),
    ("C-members-not-moved", OP, "run.members.Move(mv.BookID, pl.FromID, &sid)", "_ = sid"),
    ("C-delete-ignores-held-books", OP, "len(sr.Held) == 0 && safeToDelete &&", "safeToDelete &&"),
    ("C-delete-ignores-refs", OP, "safeToDelete := run.refCounts[pl.FromID] <= sourceBooks", "safeToDelete := true"),
    ("C-delete-ignores-series-lock", OP, "!database.SeriesHeld(src)", "true"),
    ("D-demotion-not-applied", OP, "candidates := DemotePlans(SeriesDenumber(in), authorNames)", "candidates := SeriesDenumber(in)"),
    ("D-demotion-without-author-names", OP, "DemotePlans(SeriesDenumber(in), authorNames)", "DemotePlans(SeriesDenumber(in), nil)"),
    ("D-demoted-stays-eligible", PL, "out[i].Confidence = ConfidenceLow", "out[i].Confidence = pl.Confidence"),
    ("E-limit-unwired", OP, "Limit: params.Limit})", "Limit: 0})"),
    ("E-minbooks-unwired", OP, "sdn.Selection{MinBooks: params.MinBooks,", "sdn.Selection{MinBooks: 0,"),
    ("E-basename-unwired", OP, "BaseName: params.BaseName, Limit", 'BaseName: "", Limit'),
    ("G-lenient-decoding", OP, "if err := decodeStrictParams(raw, &params); err != nil {", "if err := json.Unmarshal(raw, &params); err != nil {"),
    ("G-dry-run-applies", OP, "if !params.Apply {\n" + T*2 + "summary :=", "if false {\n" + T*2 + "summary :="),
    ("G-no-opid-guard", OP, 'if opID == "" {', "if false {"),
    ("G-no-restorer-guard", OP, "if _, ok := database.AsCapability[database.SeriesRestorer](store); !ok {", "if _, ok := database.AsCapability[database.SeriesRestorer](store); !ok && false {"),
    ("G-report-failure-ignored", OP, 'return fmt.Errorf("write report %s: %w", params.ReportPath, err)', 'log.Warn("x", "err", err)'),
    ("G-counts-fail-open", OP, 'return nil, fmt.Errorf("GetAllSeriesBookCounts: %w", cerr)', "counts = map[int]int{}"),
    ("G-refcounts-fail-open", OP, 'return nil, fmt.Errorf("series-denumber: refusing to run without unfiltered series reference counts: %w", rcErr)', "refCounts = map[int]int{}"),
    ("G-membership-fail-open", OP, 'return nil, fmt.Errorf("series-denumber: refusing to run without series membership: %w", mErr)', "_ = mErr"),
    ("R-failed-delete-not-counted", OP, "// The row stays; its undo row finds the series live and does nothing.\n" + T*5 + "failed++", "// The row stays; its undo row finds the series live and does nothing."),
    ("R-cache-not-invalidated", OP, "p.deps.InvalidateSeriesCache()", "_ = created"),
    ("R-empty-numbered-not-counted", OP, "if pl.Books == 0 && refCounts[pl.FromID] == 0 {", "if false {"),
]


def run():
    p = subprocess.run(["go", "test", "./internal/plugins/maintenance/", "-count=1", "-v", "-run", PATTERN],
                       capture_output=True, text=True)
    out = p.stdout + p.stderr
    fails = [l.strip() for l in out.splitlines() if l.startswith("--- FAIL")]
    passes = sum(1 for l in out.splitlines() if l.startswith("--- PASS"))
    build = "[build failed]" in out or "\n#" in out or out.startswith("#")
    return p.returncode, fails, passes, build, out


def main():
    check = "--check" in sys.argv
    srcs = {}
    for name, f, old, new in MUTANTS:
        s = srcs.setdefault(f, open(f).read())
        n = s.count(old)
        if n != 1:
            print("MUTANT %s: SITE-%s (%d matches)" % (name, "NOT-FOUND" if n == 0 else "AMBIGUOUS", n), flush=True)
            if check:
                continue
    if check:
        print("CHECKED %d mutants" % len(MUTANTS))
        return
    rc, fails, passes, build, out = run()
    print("BASELINE-unmutated: rc=%d passed=%d failed=%d" % (rc, passes, len(fails)), flush=True)
    if rc != 0 or passes < 40:
        print(out[-3000:])
        print("BASELINE NOT GREEN -- no mutant result below means anything", flush=True)
        sys.exit(1)
    for name, f, old, new in MUTANTS:
        s = open(f).read()
        if s.count(old) != 1:
            print("MUTANT %s: SKIPPED site not unique" % name, flush=True)
            continue
        open(f, "w").write(s.replace(old, new, 1))
        try:
            rc, fails, passes, build, out = run()
        finally:
            subprocess.run(["git", "checkout", "-q", "--", f])
        if build:
            print("MUTANT %s: INVALID build failed (not a kill) %s" % (name, " | ".join(out.splitlines()[:3])), flush=True)
        else:
            print("MUTANT %s: %s %s" % (name, "KILLED" if rc != 0 else "SURVIVED", " | ".join(fails[:3])), flush=True)
    print("ALLMUTDONE", flush=True)


main()
