// file: internal/itunes/mhoh_encoding_table_test.go
// version: 1.0.1
// guid: 3b7e1fa2-9c4d-4e58-b6f0-2a8d5c9f1e3b
// last-edited: 2026-08-20

package itunes

import (
	"testing"
)

// TestMhohEncodingTable_NonEmpty verifies that the corpus-derived table is
// populated and contains the minimum required entries for the mhoh-format
// guard. The guard REQUIRES entries for hohmTypes 0x02, 0x0B, and 0x0D —
// these are the types written by our location-update and metadata-update
// paths and are the primary CRIT-1 / SPEC 2 targets.
func TestMhohEncodingTable_NonEmpty(t *testing.T) {
	requiredTypes := []uint32{0x02, 0x0B, 0x0D}
	for _, hohmType := range requiredTypes {
		entry, ok := ITunesMhohEncoding[hohmType]
		if !ok {
			t.Errorf("ITunesMhohEncoding missing required type 0x%02X", hohmType)
			continue
		}
		if entry.Count == 0 {
			t.Errorf("ITunesMhohEncoding[0x%02X].Count == 0; expected corpus-derived count", hohmType)
		}
	}
}

// TestMhohEncodingTable_AllHeaderLen24 verifies the global corpus invariant:
// every iTunes-authored mhoh block has headerLen == 24. No entry in the table
// should deviate from this.
func TestMhohEncodingTable_AllHeaderLen24(t *testing.T) {
	for hohmType, entry := range ITunesMhohEncoding {
		if entry.HeaderLen != 24 {
			t.Errorf("ITunesMhohEncoding[0x%02X].HeaderLen = %d; want 24",
				hohmType, entry.HeaderLen)
		}
	}
}

// TestMhohEncodingTable_At27Uniform verifies that every text string type in
// the table has At27Uniform == true. This is the core CRIT-1 assertion:
// iTunes writes byte +27 as 0x00 in all text string blocks.
func TestMhohEncodingTable_At27Uniform(t *testing.T) {
	for hohmType, entry := range ITunesMhohEncoding {
		if !entry.At27Uniform {
			t.Errorf("ITunesMhohEncoding[0x%02X].At27Uniform = false; "+
				"all text string types must have At27Uniform = true (CRIT-1)",
				hohmType)
		}
	}
}

// TestMhohEncodingTable_AllowedAt24Values verifies that every entry in the
// table has at least one AllowedAt24 value, and that all allowed values are
// in the known set {0, 1, 2, 3}. Values outside this range would indicate a
// binary-blob type accidentally included in the table.
func TestMhohEncodingTable_AllowedAt24Values(t *testing.T) {
	knownValues := map[uint32]bool{0: true, 1: true, 2: true, 3: true}

	for hohmType, entry := range ITunesMhohEncoding {
		if len(entry.AllowedAt24) == 0 {
			t.Errorf("ITunesMhohEncoding[0x%02X].AllowedAt24 is empty", hohmType)
			continue
		}
		for _, v := range entry.AllowedAt24 {
			if !knownValues[v] {
				t.Errorf("ITunesMhohEncoding[0x%02X].AllowedAt24 contains %d; "+
					"only {0, 1, 2, 3} are valid encoding indicators in iTunes-authored blocks",
					hohmType, v)
			}
		}
	}
}

// TestMhohEncodingTable_LocalURLASCIIOnly verifies that the 0x0B (LocalURL)
// type only permits encoding values {0, 2} — pure ASCII / percent-encoded
// values. LocalURL is always percent-encoded (RFC 3986), so UTF-16LE (3) or
// Windows-1252 (1) must never appear.
func TestMhohEncodingTable_LocalURLASCIIOnly(t *testing.T) {
	entry, ok := ITunesMhohEncoding[0x0B]
	if !ok {
		t.Fatal("ITunesMhohEncoding missing type 0x0B (LocalURL)")
	}
	for _, v := range entry.AllowedAt24 {
		if v != 0 && v != 2 {
			t.Errorf("ITunesMhohEncoding[0x0B].AllowedAt24 contains %d; "+
				"LocalURL is always ASCII-encoded (at24 ∈ {0, 2}); "+
				"values 1 (latin1) or 3 (utf16le) would indicate a corrupt block",
				v)
		}
	}
}

// TestMhohEncodingTable_AllowedAt24Contains verifies that
// MhohEncodingEntry.AllowedAt24Contains works correctly for boundary cases.
func TestMhohEncodingTable_AllowedAt24Contains(t *testing.T) {
	entry := MhohEncodingEntry{
		HeaderLen:   24,
		AllowedAt24: []uint32{1, 3},
		At27Uniform: true,
		Count:       100,
	}

	tests := []struct {
		v    uint32
		want bool
	}{
		{0, false},
		{1, true},
		{2, false},
		{3, true},
		{4, false},
		{999, false},
	}

	for _, tt := range tests {
		got := entry.AllowedAt24Contains(tt.v)
		if got != tt.want {
			t.Errorf("AllowedAt24Contains(%d) = %v; want %v", tt.v, got, tt.want)
		}
	}
}

// TestMhohEncodingTable_CriticalTypes verifies the specific corpus-derived
// values for the types most critical to CRIT-1 / SPEC 2 correctness.
// These values were derived from 965,223 blocks in the golden iTunes library
// (version 12.13.10.3, 94,575 tracks, audited 2026-06-09).
func TestMhohEncodingTable_CriticalTypes(t *testing.T) {
	tests := []struct {
		hohmType uint32
		name     string
		wantAt24 []uint32
		minCount int
	}{
		// 0x02 Name: Latin-1 (1) and UTF-16LE (3) observed; 94,575 tracks.
		{0x02, "Name", []uint32{1, 3}, 90000},
		// 0x0B LocalURL: ASCII only ({0, 2}); 94,201 blocks.
		{0x0B, "LocalURL", []uint32{0, 2}, 90000},
		// 0x0D Location: Latin-1 and UTF-16LE; 93,014 blocks.
		{0x0D, "Location", []uint32{1, 3}, 90000},
		// 0x06 Kind: UTF-16LE only; 93,539 blocks (iTunes encodes Kind as UTF-16LE exclusively).
		{0x06, "Kind", []uint32{3}, 90000},
	}

	for _, tt := range tests {
		entry, ok := ITunesMhohEncoding[tt.hohmType]
		if !ok {
			t.Errorf("type 0x%02X (%s): missing from table", tt.hohmType, tt.name)
			continue
		}

		if entry.Count < tt.minCount {
			t.Errorf("type 0x%02X (%s): Count=%d; want >= %d",
				tt.hohmType, tt.name, entry.Count, tt.minCount)
		}

		// Verify AllowedAt24 contains exactly the expected values (no more, no fewer).
		wantSet := make(map[uint32]bool)
		for _, v := range tt.wantAt24 {
			wantSet[v] = true
		}
		gotSet := make(map[uint32]bool)
		for _, v := range entry.AllowedAt24 {
			gotSet[v] = true
		}
		for v := range wantSet {
			if !gotSet[v] {
				t.Errorf("type 0x%02X (%s): AllowedAt24 missing value %d",
					tt.hohmType, tt.name, v)
			}
		}
		for v := range gotSet {
			if !wantSet[v] {
				t.Errorf("type 0x%02X (%s): AllowedAt24 has unexpected value %d",
					tt.hohmType, tt.name, v)
			}
		}
	}
}

// TestMhohEncodingTable_DeriveFromAudit exercises DeriveEncodingTable with a
// synthetic audit report and verifies that the derivation produces a correct
// table. This tests the DeriveEncodingTable function used when the audit tool
// is run against a real library.
func TestMhohEncodingTable_DeriveFromAudit(t *testing.T) {
	report := &MhohAuditReport{
		LibraryVersion:  "12.13.10.3",
		TotalMhohBlocks: 100,
		PerType: map[string]*MhohTypeAudit{
			"2": {
				HohmTypeHex:     "0x02",
				Count:           60,
				HeaderLenValues: map[string]int{"24": 60},
				At24Values:      map[string]int{"1": 50, "3": 10},
				At27Values:      map[string]int{"0": 60},
				TailZero:        map[string]int{"true": 60},
				LenArithmeticOK: map[string]int{"true": 60},
			},
			"11": {
				HohmTypeHex:     "0x0B",
				Count:           40,
				HeaderLenValues: map[string]int{"24": 40},
				At24Values:      map[string]int{"0": 20, "2": 20},
				At27Values:      map[string]int{"0": 40},
				TailZero:        map[string]int{"true": 40},
				LenArithmeticOK: map[string]int{"true": 40},
			},
			// Non-uniform headerLen — should be excluded from derived table.
			"13": {
				HohmTypeHex:     "0x0D",
				Count:           30,
				HeaderLenValues: map[string]int{"24": 20, "41": 10}, // mixed — exclude
				At24Values:      map[string]int{"1": 30},
				At27Values:      map[string]int{"0": 30},
				TailZero:        map[string]int{"true": 30},
				LenArithmeticOK: map[string]int{"true": 30},
			},
		},
	}

	table := DeriveEncodingTable(report)

	// Type 2 (Name) should be in the table.
	e, ok := table[2]
	if !ok {
		t.Fatal("DeriveEncodingTable: type 2 missing from derived table")
	}
	if e.HeaderLen != 24 {
		t.Errorf("derived table[2].HeaderLen = %d; want 24", e.HeaderLen)
	}
	if !e.AllowedAt24Contains(1) || !e.AllowedAt24Contains(3) {
		t.Errorf("derived table[2].AllowedAt24 = %v; want {1, 3}", e.AllowedAt24)
	}
	if !e.At27Uniform {
		t.Errorf("derived table[2].At27Uniform = false; want true")
	}

	// Type 11 (LocalURL) should be in the table with {0, 2}.
	e11, ok := table[11]
	if !ok {
		t.Fatal("DeriveEncodingTable: type 11 (0x0B) missing from derived table")
	}
	if !e11.AllowedAt24Contains(0) || !e11.AllowedAt24Contains(2) {
		t.Errorf("derived table[11].AllowedAt24 = %v; want {0, 2}", e11.AllowedAt24)
	}

	// Type 13 (Location) should NOT be in the derived table (non-uniform headerLen).
	if _, ok := table[13]; ok {
		t.Errorf("DeriveEncodingTable: type 13 (0x0D) should be excluded due to non-uniform headerLen")
	}
}
