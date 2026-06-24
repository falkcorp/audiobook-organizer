// file: internal/plugins/maintenance/dedup_triage_test.go
// version: 1.1.0
// guid: 8f9a0b1c-2d3e-4f50-a6b7-c8d9e0f12345
// last-edited: 2026-06-24

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

func makeBook(id string, fileSize int64, duration int, itunesPID string) *database.Book {
	b := &database.Book{ID: id, Title: "Book " + id}
	if fileSize >= 0 {
		b.FileSize = &fileSize
	}
	if duration >= 0 {
		b.Duration = &duration
	}
	if itunesPID != "" {
		b.ITunesPersistentID = &itunesPID
	}
	return b
}

func sig(kind unified.SignalKind) unified.Signal {
	return unified.Signal{Kind: kind, Confidence: 0.99}
}

func withBreakdown(c database.DedupCandidate, signals ...unified.Signal) database.DedupCandidate {
	c.ScoreBreakdown = &unified.UnifiedDedupScore{Signals: signals}
	return c
}

func TestClassifyCandidate_Genuine_FileHash(t *testing.T) {
	a := makeBook("a", 10*1024*1024, 3600, "")
	b := makeBook("b", 10*1024*1024, 3600, "")
	c := withBreakdown(database.DedupCandidate{Layer: "exact"}, sig(unified.SigExactFile))

	cls, reason := ClassifyCandidate(c, a, b)
	if cls != TriageClassGenuine {
		t.Errorf("got %s (%s), want genuine", cls, reason)
	}
}

func TestClassifyCandidate_Genuine_ISBN(t *testing.T) {
	a := makeBook("a", 10*1024*1024, 3600, "")
	b := makeBook("b", 10*1024*1024, 3600, "")
	c := withBreakdown(database.DedupCandidate{Layer: "exact"}, sig(unified.SigISBNASIN))

	cls, _ := ClassifyCandidate(c, a, b)
	if cls != TriageClassGenuine {
		t.Errorf("got %s, want genuine", cls)
	}
}

func TestClassifyCandidate_Stub_TinyFile(t *testing.T) {
	a := makeBook("a", 100, 1, "") // 100 bytes, 1s — byte-empty stub
	b := makeBook("b", 10*1024*1024, 3600, "")
	c := withBreakdown(database.DedupCandidate{Layer: "exact"}, sig(unified.SigMetaFuzzy))

	cls, _ := ClassifyCandidate(c, a, b)
	if cls != TriageClassStub {
		t.Errorf("got %s, want stub", cls)
	}
}

func TestClassifyCandidate_Stub_ZeroDuration(t *testing.T) {
	a := makeBook("a", 1024, 0, "") // below threshold, no duration
	b := makeBook("b", 10*1024*1024, 3600, "")
	c := withBreakdown(database.DedupCandidate{Layer: "exact"}, sig(unified.SigMetaFuzzy))

	cls, _ := ClassifyCandidate(c, a, b)
	if cls != TriageClassStub {
		t.Errorf("got %s, want stub", cls)
	}
}

func TestClassifyCandidate_Stub_LargeFileRealDuration_NotStub(t *testing.T) {
	// File within threshold but has real duration — not a stub
	a := makeBook("a", 100*1024, 120, "") // 100 KiB but 2 min audio
	b := makeBook("b", 10*1024*1024, 3600, "")
	c := withBreakdown(database.DedupCandidate{Layer: "exact"}, sig(unified.SigMetaFuzzy))

	cls, _ := ClassifyCandidate(c, a, b)
	if cls == TriageClassStub {
		t.Errorf("book with real duration should not be classified as stub")
	}
}

func TestClassifyCandidate_Fragment_DurationRatio(t *testing.T) {
	// Short: 2 min chapter vs 90 min full book = 2.2% ratio < 5%
	a := makeBook("a", 5*1024*1024, 120, "itunes-pid-a")
	b := makeBook("b", 100*1024*1024, 5400, "itunes-pid-b")
	c := database.DedupCandidate{Layer: "exact"} // no ScoreBreakdown (pre-T015)

	cls, reason := ClassifyCandidate(c, a, b)
	if cls != TriageClassFragment {
		t.Errorf("got %s (%s), want fragment", cls, reason)
	}
}

func TestClassifyCandidate_Fragment_Cons17Suspect_NotFragment(t *testing.T) {
	// CONS-17: one book has an astronomically large duration (stored as ms instead
	// of seconds) with no DurationVerifiedAt. The ratio looks tiny (<5%) but the
	// duration data is unreliable — must fall through to unknown, not fragment.
	a := makeBook("a", 50*1024*1024, 31431, "")   // correct: ~8.7h
	b := makeBook("b", 50*1024*1024, 31430435, "") // CONS-17: 31430435s = 364 days
	c := database.DedupCandidate{Layer: "exact"}

	cls, reason := ClassifyCandidate(c, a, b)
	if cls == TriageClassFragment {
		t.Errorf("CONS-17 suspect book must not be classified as fragment, got fragment (%s)", reason)
	}
	if cls != TriageClassUnknown {
		t.Errorf("CONS-17 suspect book should fall to unknown, got %s (%s)", cls, reason)
	}
}

func TestClassifyCandidate_Fragment_EqualDuration_NotFragment(t *testing.T) {
	// Same duration — clearly not a fragment
	a := makeBook("a", 5*1024*1024, 3600, "")
	b := makeBook("b", 5*1024*1024, 3600, "")
	c := database.DedupCandidate{Layer: "exact"}

	cls, _ := ClassifyCandidate(c, a, b)
	if cls == TriageClassFragment {
		t.Errorf("same-duration books should not be classified as fragment")
	}
}

func TestClassifyCandidate_TitleLeak(t *testing.T) {
	// Both iTunes, exact layer, no hard signal in breakdown
	a := makeBook("a", 5*1024*1024, 3600, "itunes-pid-a")
	b := makeBook("b", 5*1024*1024, 3600, "itunes-pid-b")
	c := withBreakdown(database.DedupCandidate{Layer: "exact"}, sig(unified.SigMetaFuzzy))

	cls, reason := ClassifyCandidate(c, a, b)
	if cls != TriageClassTitleLeak {
		t.Errorf("got %s (%s), want title_leak", cls, reason)
	}
}

func TestClassifyCandidate_Unknown_NilBreakdown(t *testing.T) {
	// Pre-T015: no ScoreBreakdown, not a stub or fragment
	a := makeBook("a", 5*1024*1024, 3600, "")
	b := makeBook("b", 5*1024*1024, 3600, "")
	c := database.DedupCandidate{Layer: "exact", ScoreBreakdown: nil}

	cls, _ := ClassifyCandidate(c, a, b)
	if cls != TriageClassUnknown {
		t.Errorf("got %s, want unknown for pre-T015 candidate", cls)
	}
}

func TestClassifyCandidate_MissingBook(t *testing.T) {
	a := makeBook("a", 5*1024*1024, 3600, "")
	c := database.DedupCandidate{Layer: "exact"}

	cls, _ := ClassifyCandidate(c, a, nil)
	if cls != TriageClassUnknown {
		t.Errorf("nil book should yield unknown, got %s", cls)
	}
}

func TestIsPurgeable(t *testing.T) {
	purge := []TriageClass{TriageClassStub, TriageClassFragment, TriageClassTitleLeak}
	keep := []TriageClass{TriageClassGenuine, TriageClassUnknown}

	for _, cls := range purge {
		if !IsPurgeable(cls) {
			t.Errorf("IsPurgeable(%s) = false, want true", cls)
		}
	}
	for _, cls := range keep {
		if IsPurgeable(cls) {
			t.Errorf("IsPurgeable(%s) = true, want false", cls)
		}
	}
}
