// file: internal/plugins/maintenance/isbn_enrichment_retired_test.go
// version: 1.0.0
// guid: 0b5f2e83-7c4a-4d19-9e61-a8d3c27f4b50
// last-edited: 2026-10-02

package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// maintenance.isbn-enrichment is retired: still registered, refuses to run,
// and has no cron Schedule. It refuses before touching deps, so a nil-deps
// plugin is enough.
func TestISBNEnrichmentDef_Retired(t *testing.T) {
	p := &Plugin{}
	def := p.isbnEnrichmentDef()
	if def.ID != "maintenance.isbn-enrichment" || def.Schedule != nil {
		t.Fatalf("def = %+v", def)
	}
	err := def.Run(context.Background(), nil, nil)
	if !errors.Is(err, errISBNEnrichmentRetired) || !strings.Contains(err.Error(), "retired: use metafetch.asin-backfill") {
		t.Fatalf("err = %v", err)
	}
}
