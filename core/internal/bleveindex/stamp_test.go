// file: core/internal/bleveindex/stamp_test.go
// version: 0.1.0
// guid: 1a7f3e92-c6d4-4b85-9f20-e8b5a3c71d46
// last-edited: 2026-11-20

package bleveindex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/falkcorp/mediacore/storage/index"
)

// TestStampOfASpecWithoutAnalyzersIsUnchanged: analyzers and boosts must not
// move the fingerprint of a Spec that uses neither, or adding them would have
// recreated (and rebuilt) every index built before them. The expected value is
// the fingerprint as it was computed before Spec.Analyzers existed.
func TestStampOfASpecWithoutAnalyzersIsUnchanged(t *testing.T) {
	s := index.Spec{MappingVersion: "7", Fields: villainFields()}
	var decl []byte
	for _, f := range s.Fields {
		decl = fmt.Appendf(decl, "%q %d %q %t %t\n", f.Name, f.Kind, f.Analyzer, f.Store, f.Facet)
	}
	sum := sha256.Sum256(decl)
	if got, want := mappingStamp(s), "7\n"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("stamp = %q, want the pre-analyzer %q", got, want)
	}
	boosted := s
	boosted.Fields = append([]index.Field(nil), s.Fields...)
	boosted.Fields[0].Boost = 3
	if mappingStamp(boosted) != mappingStamp(s) {
		t.Fatal("a boost moved the mapping fingerprint")
	}
	analysed := s
	analysed.Analyzers = []index.Analyzer{{Name: "casefile", Tokenizer: index.TokenizeWholeValue}}
	if mappingStamp(analysed) == mappingStamp(s) {
		t.Fatal("an analyzer declaration did not move the mapping fingerprint")
	}
	edited := analysed
	edited.Analyzers = []index.Analyzer{{Name: "casefile", Tokenizer: index.TokenizeWholeValue, TokenFilters: []index.TokenFilter{index.FilterLowercase}}}
	if mappingStamp(edited) == mappingStamp(analysed) {
		t.Fatal("an analyzer edit did not move the mapping fingerprint")
	}
}
