// file: core/kernel/cutover_offline_test.go
// version: 1.0.0
// guid: fbaaf5f5-4748-4ad9-911c-cebcac5ebf03
// last-edited: 2026-11-11

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// TestOfflineQuarantineCommands (05-storage §5S.3.13; `storage quarantine
// list|export|purge`): after a cutover that quarantined rows, the offline
// open lists them (one step's or every step's) with their reasons, exports
// the plain rows' values as JSON Lines and refuses the sealed ones with no
// unsealer rather than omit them, and a purge with no secret deleter for the
// sealed rows fails closed, deleting nothing.
func TestOfflineQuarantineCommands(t *testing.T) {
	h, c := dryRunHost(t, cutoverRows(10, 2))
	_, q := liveCutover(t, h.ctx, c)
	if len(q) != 4 { // files 5 and 10 are undecodable, both vaults are sensitive
		t.Fatalf("fixture: the cutover quarantined %d rows, want 4", len(q))
	}

	ro, err := OpenOffline(h.ctx, c, true)
	if err != nil {
		t.Fatal(err)
	}
	all, err := ro.QuarantineList(h.ctx, "")
	if err != nil || len(all) != 4 {
		t.Fatalf("list = %d entries, %v; want 4", len(all), err)
	}
	one, err := ro.QuarantineList(h.ctx, "gcpd.case-files")
	if err != nil || len(one) != 4 {
		t.Fatalf("list of the step = %d entries, %v; want 4", len(one), err)
	}
	if none, err := ro.QuarantineList(h.ctx, "robin.import"); err != nil || len(none) != 0 {
		t.Errorf("list of another step = %d entries, %v; want none", len(none), err)
	}
	var sealed int
	for _, e := range all {
		if e.Sealed() {
			sealed++
		} else if !strings.Contains(e.Record.Reason, "Joker venom") {
			t.Errorf("plain record %d has reason %q", e.N, e.Record.Reason)
		}
	}
	if sealed != 2 {
		t.Errorf("%d sealed entries, want the 2 vault rows", sealed)
	}
	var out bytes.Buffer
	n, err := ro.QuarantineExport(h.ctx, "gcpd.case-files", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "no unsealer") || n >= 4 {
		t.Errorf("export with sealed rows and no unsealer = %d, %v; want a refusal before the end", n, err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var r storage.QuarantineRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.Sensitive || !strings.HasPrefix(string(r.Key), "case:file/") {
			t.Errorf("exported line %q (%v) is not a plain case file row", line, err)
		}
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}

	rw, err := OpenOffline(h.ctx, c, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func(o *Offline) { _ = o.Close() }(rw)
	if n, err := rw.QuarantinePurge(context.Background(), "gcpd.case-files", nil, "alfred"); err == nil || n != 0 {
		t.Errorf("purge of sealed rows with no secret deleter = %d, %v; want a refusal", n, err)
	}
	if left, err := rw.QuarantineList(h.ctx, ""); err != nil || len(left) != 4 {
		t.Errorf("after the refused purge %d records remain, %v; want all 4", len(left), err)
	}
}
