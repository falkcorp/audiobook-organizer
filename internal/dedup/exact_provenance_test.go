// file: internal/dedup/exact_provenance_test.go
// version: 1.0.1
// guid: 29b011fa-6878-4516-b7f1-e680f303d4ea
// last-edited: 2026-09-28

// Tests for the placeholder-title guard and exact-rule provenance
// (exact_provenance.go). The fixtures mirror the production pairs of
// 2026-09-27: books titled "read by narrator" / "Unknown Title" hanging off
// author rows NAMED "read by narrator" / "Unknown Title".

package dedup

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// testExactEvidence is the rule the pre-existing guard tests pass to
// upsertExactCandidate: those tests exercise the gates, not the provenance.
var testExactEvidence = exactEvidence{rule: "test", detail: "test fixture", raw: 1}

const bigFile = int64(200_000_000)

func sizedBook(id, title string, authorID int) *database.Book {
	sz := bigFile
	a := authorID
	return &database.Book{ID: id, Title: title, AuthorID: &a, FileSize: &sz}
}

// wireAuthorBooks makes every book resolvable by ID and lists them all under
// their (single) author, with the given author name.
func wireAuthorBooks(mock *database.MockStore, authorName string, books ...*database.Book) {
	byID := map[string]*database.Book{}
	cores := make([]database.BookCore, 0, len(books))
	for _, b := range books {
		byID[b.ID] = b
		cores = append(cores, b.Core())
	}
	mock.GetBooksByAuthorIDCoreFunc = func(int) ([]database.BookCore, error) { return cores, nil }
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return byID[id], nil }
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: authorName}, nil
	}
}

func onlyCandidate(t *testing.T, es *database.EmbeddingStore) database.DedupCandidate {
	t.Helper()
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("want exactly 1 candidate, got %d", len(cands))
	}
	return cands[0]
}

func countCandidates(t *testing.T, es *database.EmbeddingStore) int {
	t.Helper()
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	return len(cands)
}

// ruleSignal returns the exact_rule signal on c, failing if there is none.
func ruleSignal(t *testing.T, c database.DedupCandidate) unified.Signal {
	t.Helper()
	if c.ScoreBreakdown == nil {
		t.Fatalf("candidate %d has no breakdown: the Why panel would say 'No score breakdown recorded'", c.ID)
	}
	if !c.ScoreBreakdown.IsProvenanceOnly() {
		t.Fatalf("exact candidate breakdown must be provenance-only (no score): %+v", c.ScoreBreakdown)
	}
	for _, s := range c.ScoreBreakdown.Signals {
		if s.Kind == unified.SigExactRule {
			return s
		}
	}
	t.Fatalf("no exact_rule signal in %+v", c.ScoreBreakdown.Signals)
	return unified.Signal{}
}

func TestTitleEvidenceRefusal(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		aT, bT, aAuth, bAuth string
		refused              bool
	}{
		{"real pair", "Dragon Heart", "Dragon Heart", "Cecelia Holland", "Cecelia Holland", false},
		{"read by narrator title", "read by narrator", "read by narrator", "Mark", "Mark", true},
		{"unknown title one side", "Unknown Title", "The Long Earth", "A", "B", true},
		{"placeholder authors both sides", "Dragon Heart", "Dragon Heart", "Unknown Author", "read by narrator", true},
		{"placeholder author one side only", "Dragon Heart", "Dragon Heart", "Unknown Author", "Cecelia Holland", false},
		{"unresolved author is not a placeholder", "Dragon Heart", "Dragon Heart", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := titleEvidenceRefusal(tc.aT, tc.bT, tc.aAuth, tc.bAuth) != ""
			if got != tc.refused {
				t.Fatalf("refused=%v, want %v", got, tc.refused)
			}
		})
	}
}

// The production shape: both books under ONE author row named "read by
// narrator", both titled "read by narrator". checkExactTitle paired them at
// Levenshtein 0.
func TestExactTitle_NeverPairsPlaceholderTitles(t *testing.T) {
	for _, tc := range []struct{ title, author string }{
		{"read by narrator", "read by narrator"},
		{"Unknown Title", "Unknown Title"},
		{"Unknown Title", "Terry Pratchett"}, // placeholder title alone is enough
		{"", "Terry Pratchett"},
	} {
		t.Run(tc.title+"/"+tc.author, func(t *testing.T) {
			engine, mock, es := setupTestEngine(t)
			a := sizedBook("book-a", tc.title, 57768)
			b := sizedBook("book-b", tc.title, 57768)
			wireAuthorBooks(mock, tc.author, a, b)
			if err := engine.checkExactTitle(a, tc.author); err != nil {
				t.Fatal(err)
			}
			if n := countCandidates(t, es); n != 0 {
				t.Fatalf("placeholder pair emitted %d candidates, want 0", n)
			}
		})
	}
}

func TestExactTitle_NeverPairsUnderPlaceholderAuthor(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	a := sizedBook("book-a", "Dragon Heart", 7)
	b := sizedBook("book-b", "Dragon Heart", 7)
	wireAuthorBooks(mock, "Unknown Author", a, b)
	if err := engine.checkExactTitle(a, "Unknown Author"); err != nil {
		t.Fatal(err)
	}
	if n := countCandidates(t, es); n != 0 {
		t.Fatalf("title shared under a placeholder author emitted %d candidates, want 0", n)
	}
}

func TestExactTitle_RealPairStillEmittedWithRule(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	a := sizedBook("book-a", "Departure from the Script", 2)
	b := sizedBook("book-b", "Departure from the Script", 2)
	wireAuthorBooks(mock, "Jae", a, b)
	if err := engine.checkExactTitle(a, "Jae"); err != nil {
		t.Fatal(err)
	}
	c := onlyCandidate(t, es)
	if c.Layer != "exact" {
		t.Fatalf("layer = %q, want exact", c.Layer)
	}
	s := ruleSignal(t, c)
	if s.Rule != ExactRuleTitleAuthor {
		t.Fatalf("rule = %q, want %q", s.Rule, ExactRuleTitleAuthor)
	}
	for _, want := range []string{`"Jae"`, "departure from the script", "edit distance 0"} {
		if !strings.Contains(s.Evidence, want) {
			t.Errorf("evidence %q missing %q", s.Evidence, want)
		}
	}
}

func durationPairFixture(t *testing.T, title, author string, secA, secB int) (*Engine, *database.EmbeddingStore, *database.Book) {
	t.Helper()
	engine, mock, es := setupTestEngine(t)
	a := sizedBook("book-a", title, 3)
	b := sizedBook("book-b", title, 3)
	a.Duration, b.Duration = &secA, &secB
	wireAuthorBooks(mock, author, a, b)
	mock.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		if id == "book-a" {
			return chapterRows(id, 1, secA, 1), nil
		}
		return chapterRows(id, 1, secB, 1), nil
	}
	return engine, es, a
}

func TestDurationMatch_NeverPairsPlaceholders(t *testing.T) {
	for _, tc := range []struct{ title, author string }{
		{"read by narrator", "Mark"},
		{"Splashdown", "Unknown Author"},
		{"Splashdown", "read by narrator"},
	} {
		t.Run(tc.title+"/"+tc.author, func(t *testing.T) {
			engine, es, a := durationPairFixture(t, tc.title, tc.author, 3515, 3503)
			if err := engine.checkDurationMatch(a, tc.author); err != nil {
				t.Fatal(err)
			}
			if n := countCandidates(t, es); n != 0 {
				t.Fatalf("emitted %d candidates, want 0", n)
			}
		})
	}
}

func TestDurationMatch_RealPairRecordsRule(t *testing.T) {
	engine, es, a := durationPairFixture(t, "Splashdown", "Blaine L. Pardoe", 40665, 40600)
	if err := engine.checkDurationMatch(a, "Blaine L. Pardoe"); err != nil {
		t.Fatal(err)
	}
	s := ruleSignal(t, onlyCandidate(t, es))
	if s.Rule != ExactRuleDurationTitle {
		t.Fatalf("rule = %q, want %q", s.Rule, ExactRuleDurationTitle)
	}
	for _, want := range []string{"40665s", "40600s", "limit 2%", `"Blaine L. Pardoe"`} {
		if !strings.Contains(s.Evidence, want) {
			t.Errorf("evidence %q missing %q", s.Evidence, want)
		}
	}
}

// Identical bytes are content evidence: a placeholder-titled pair still pairs
// on a file hash, and the evidence says the title was not what paired them.
func TestFileHash_PlaceholderPairStillEmittedWithRule(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false
	a := sizedBook("book-a", "read by narrator", 1)
	b := sizedBook("book-b", "read by narrator", 1)
	wireAuthorBooks(mock, "read by narrator", a, b)
	h := "4d0fb53d88268abda448a2eff507cd36ca9ed409fb86c2b67419b30cc0ed7aaf"
	if _, err := engine.handleFileHashMatch(a, b, "read by narrator", fileHashMatch{hash: h, fileID: "file-1"}); err != nil {
		t.Fatal(err)
	}
	s := ruleSignal(t, onlyCandidate(t, es))
	if s.Rule != ExactRuleFileHash {
		t.Fatalf("rule = %q, want %q", s.Rule, ExactRuleFileHash)
	}
	for _, want := range []string{h[:16], "file file-1", "NOT used as evidence"} {
		if !strings.Contains(s.Evidence, want) {
			t.Errorf("evidence %q missing %q", s.Evidence, want)
		}
	}
}

func TestISBN_RecordsSharedIdentifier(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	asin := "B07SJHNC7Y"
	a := sizedBook("book-a", "Orion Colony", 1)
	b := sizedBook("book-b", "Orion Colony", 2)
	a.ASIN, b.ASIN = &asin, &asin
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		if offset == 0 {
			return []database.Book{*a, *b}, nil
		}
		return nil, nil
	}
	if err := engine.checkExactISBN(a); err != nil {
		t.Fatal(err)
	}
	s := ruleSignal(t, onlyCandidate(t, es))
	if s.Rule != ExactRuleISBNASIN || !strings.Contains(s.Evidence, "ASIN "+asin) {
		t.Fatalf("rule/evidence = %q / %q", s.Rule, s.Evidence)
	}
}

func TestMetadataHash_RecordsRule(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	msh := "audible:B0TESTHASH"
	a := sizedBook("book-a", "Some Book", 1)
	b := sizedBook("book-b", "Some Book", 2)
	a.MetadataSourceHash, b.MetadataSourceHash = &msh, &msh
	mock.GetBooksByMetadataSourceHashFunc = func(string) ([]database.Book, error) {
		return []database.Book{*a, *b}, nil
	}
	if err := engine.checkExactMetadataSourceHash(a); err != nil {
		t.Fatal(err)
	}
	if s := ruleSignal(t, onlyCandidate(t, es)); s.Rule != ExactRuleMetadataHash {
		t.Fatalf("rule = %q", s.Rule)
	}
}

// A future emitter that forgets the rule must fail loudly, not write an
// unexplained row.
func TestUpsertExactCandidate_RefusesMissingRule(t *testing.T) {
	engine, _, es := setupTestEngine(t)
	a := sizedBook("book-a", "X Y Z", 1)
	b := sizedBook("book-b", "X Y Z", 1)
	if err := engine.upsertExactCandidate(a, b, "exact", 1.0, exactEvidence{}); err == nil {
		t.Fatal("want an error for a rule-less exact candidate")
	}
	if n := countCandidates(t, es); n != 0 {
		t.Fatalf("rule-less candidate was written (%d rows)", n)
	}
}

// Metadata-fuzzy is title evidence too.
func TestCollectMetaFuzzy_SkipsPlaceholders(t *testing.T) {
	_, mock, _ := setupTestEngine(t)
	q := sizedBook("q", "Dragon Heart", 1)
	other := sizedBook("o", "Dragon Heart", 2)
	ph := sizedBook("p", "read by narrator", 2)
	byID := map[string]*database.Book{"o": other, "p": ph}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return byID[id], nil }

	authorName := "Unknown Author"
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: authorName}, nil
	}
	sigs, err := CollectMetaFuzzy(mock, q, "Unknown Author", []string{"o", "p"}, DefaultMetaFuzzyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 0 {
		t.Fatalf("placeholder authors / title produced %d fuzzy signals, want 0", len(sigs))
	}

	// Control: the same titles under real authors do produce a signal.
	authorName = "Cecelia Holland"
	sigs, err = CollectMetaFuzzy(mock, q, "Cecelia Holland", []string{"o", "p"}, DefaultMetaFuzzyConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 {
		t.Fatalf("real pair produced %d fuzzy signals, want 1 (and none for the placeholder title)", len(sigs))
	}
}

// ─── drain ───────────────────────────────────────────────────────────────────

type provBook struct {
	id, title string
	authorID  int
	hash      string
	fileHash  string
	asin      *string
}

func setupProvenanceDrain(t *testing.T, authors map[int]string, books ...provBook) (*Engine, *database.EmbeddingStore) {
	t.Helper()
	engine, mock, es := setupTestEngine(t)
	byID := map[string]provBook{}
	for _, b := range books {
		byID[b.id] = b
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		b, ok := byID[id]
		if !ok {
			return nil, nil
		}
		aid := b.authorID
		out := &database.Book{ID: b.id, Title: b.title, AuthorID: &aid, ASIN: b.asin}
		if b.hash != "" {
			h := b.hash
			out.FileHash = &h
		}
		return out, nil
	}
	mock.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		b := byID[id]
		f := database.BookFile{ID: id + "-f0", BookID: id, Duration: 3600}
		f.FileHash = b.fileHash
		return []database.BookFile{f}, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: authors[id]}, nil
	}
	return engine, es
}

func TestDrainStale_PlaceholderTitle(t *testing.T) {
	asin := "B0SHARED01"
	engine, es := setupProvenanceDrain(t,
		map[int]string{57768: "read by narrator", 49447: "Unknown Title", 9: "Terry Pratchett", 5: "Unknown Author"},
		// 1: the production shape — placeholder title, placeholder author, no shared content.
		provBook{id: "N1", title: "read by narrator", authorID: 57768, hash: "h-n1"},
		provBook{id: "N2", title: "read by narrator", authorID: 57768, hash: "h-n2"},
		// 2: "Unknown Title" pair, different files.
		provBook{id: "U1", title: "Unknown Title", authorID: 49447, hash: "h-u1"},
		provBook{id: "U2", title: "Unknown Title", authorID: 49447, hash: "h-u2"},
		// 3: placeholder title but the SAME file content — kept.
		provBook{id: "S1", title: "read by narrator", authorID: 57768, hash: "h-s1", fileHash: "shared-bytes"},
		provBook{id: "S2", title: "read by narrator", authorID: 57768, hash: "h-s2", fileHash: "shared-bytes"},
		// 4: placeholder authors, real title, shared ASIN — kept.
		provBook{id: "I1", title: "Dragon Heart", authorID: 5, asin: &asin},
		provBook{id: "I2", title: "Dragon Heart", authorID: 5, asin: &asin},
		// 5: placeholder authors, real title, nothing shared — drained.
		provBook{id: "P1", title: "Splashdown", authorID: 5, hash: "h-p1"},
		provBook{id: "P2", title: "Splashdown", authorID: 5, hash: "h-p2"},
		// 6: a real pair — kept.
		provBook{id: "R1", title: "The Long Earth", authorID: 9, hash: "h-r1"},
		provBook{id: "R2", title: "The Long Earth", authorID: 9, hash: "h-r2"},
		// 7: placeholder pair whose row RECORDS a file-hash rule — kept.
		provBook{id: "F1", title: "Unknown Title", authorID: 49447, hash: "h-f1"},
		provBook{id: "F2", title: "Unknown Title", authorID: 49447, hash: "h-f2"},
	)
	for _, p := range [][2]string{{"N1", "N2"}, {"U1", "U2"}, {"S1", "S2"}, {"I1", "I2"}, {"P1", "P2"}, {"R1", "R2"}} {
		seedDrainCandidate(t, es, p[0], p[1])
	}
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "F1", EntityBID: "F2", Layer: "exact", Status: "pending",
		ScoreBreakdown: ExactRuleBreakdown("F1", "F2", ExactRuleFileHash, "Identical file content", 1),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.ReasonCounts[drainReasonPlaceholderTitle]; got != 3 {
		t.Fatalf("placeholder_title = %d, want 3 (N, U, P); counts %v", got, res.ReasonCounts)
	}
	if res.WouldPurge != 3 || res.Kept != 4 {
		t.Fatalf("would_purge=%d kept=%d, want 3/4", res.WouldPurge, res.Kept)
	}
	got := map[string]bool{}
	for _, s := range res.Samples[drainReasonPlaceholderTitle] {
		got[s.BookAID] = true
	}
	for _, want := range []string{"N1", "U1", "P1"} {
		if !got[want] {
			t.Errorf("sample for %s missing: %v", want, res.Samples[drainReasonPlaceholderTitle])
		}
	}

	// Apply reclassifies to stale-drain (a machine status), not "dismissed".
	res, err = engine.DrainStaleCandidates(context.Background(), "", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.WouldPurge != 3 {
		t.Fatalf("apply would_purge = %d, want 3", res.WouldPurge)
	}
	drained, _, err := es.ListCandidates(database.CandidateFilter{Status: staleDrainStatus, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(drained) != 3 {
		t.Fatalf("stale-drain rows = %d, want 3", len(drained))
	}
	dismissed, _, _ := es.ListCandidates(database.CandidateFilter{Status: "dismissed", Limit: 100})
	if len(dismissed) != 0 {
		t.Fatalf("drain must not record a human dismissal; got %d dismissed", len(dismissed))
	}
}

// A backfilled breakdown on a placeholder pair (production candidate 509774:
// metadata_fuzzy 0.85 on "read by narrator" + a duration signal) is still
// title-only evidence. Supporting kinds never count as content, even with a
// positive confidence.
func TestDrainStale_PlaceholderWithTitleOnlyBreakdownIsDrained(t *testing.T) {
	engine, es := setupProvenanceDrain(t,
		map[int]string{57768: "read by narrator"},
		provBook{id: "M1", title: "read by narrator", authorID: 57768, hash: "h-m1"},
		provBook{id: "M2", title: "read by narrator", authorID: 57768, hash: "h-m2"},
	)
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "M1", EntityBID: "M2", Layer: "exact", Status: "pending",
		Band: "MEDIUM",
		ScoreBreakdown: &unified.UnifiedDedupScore{
			Pair: [2]string{"M1", "M2"}, Score: 89, Band: "MEDIUM",
			Signals: []unified.Signal{
				{Kind: unified.SigDuration, Raw: 0.83, Confidence: 0.35, Evidence: "duration match 0.34%"},
				{Kind: unified.SigMetaFuzzy, Raw: 1, Confidence: 0.85, Evidence: "metadata fuzzy title+author sim 1.0"},
				{Kind: unified.SigFolderPath, Raw: 1, Confidence: 0.2, Evidence: "folder"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ReasonCounts[drainReasonPlaceholderTitle] != 1 {
		t.Fatalf("counts = %v, want placeholder_title=1", res.ReasonCounts)
	}
}

// Rescore has nothing to re-band on a provenance-only row and must count it
// as skipped rather than composing a score of 0 for it.
func TestRescore_SkipsProvenanceOnly(t *testing.T) {
	engine, _, es := setupTestEngine(t)
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Status: "pending",
		ScoreBreakdown: ExactRuleBreakdown("a", "b", ExactRuleTitleAuthor, "x", 0),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := engine.Rescore(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Changed != 0 {
		t.Fatalf("skipped=%d changed=%d, want 1/0", res.Skipped, res.Changed)
	}
}
