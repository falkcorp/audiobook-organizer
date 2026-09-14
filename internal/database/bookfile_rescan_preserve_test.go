// file: internal/database/bookfile_rescan_preserve_test.go
// version: 1.3.0
// guid: 65fa9323-39c2-4f89-a480-e74c18c02036
// last-edited: 2026-09-13

package database

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// fillBookFileNonZero sets every settable field of *bf to a distinct non-zero
// value derived from its name, recursing into plain structs (ScanState). It is
// reflection-driven so a newly added BookFile field is seeded automatically and
// the survival assertions below cover it without an edit.
func fillBookFileNonZero(t *testing.T, bf *BookFile) {
	t.Helper()
	fillValueNonZero(t, reflect.ValueOf(bf).Elem(), "BookFile")
}

func fillValueNonZero(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("v-" + path)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(int64(7 + len(path)))
	case reflect.Float64, reflect.Float32:
		v.SetFloat(0.5 + float64(len(path)))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			t.Fatalf("%s: unhandled slice type %s — extend fillValueNonZero", path, v.Type())
		}
		v.SetBytes([]byte{1, 2, 3, byte(len(path))})
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillValueNonZero(t, k, path+".key")
		e := reflect.New(v.Type().Elem()).Elem()
		fillValueNonZero(t, e, path+".val")
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillValueNonZero(t, p.Elem(), path)
		v.Set(p)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Date(2020, 1, 2, 3, 4, len(path)%60, 0, time.UTC)))
			return
		}
		for i := range v.NumField() {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			fillValueNonZero(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	default:
		t.Fatalf("%s: unhandled kind %s — extend fillValueNonZero", path, v.Kind())
	}
}

// Every BookFile field must be classified in bookFileFieldClasses, and every
// entry there must name a real field. Adding a BookFile field without deciding
// how the write paths merge it fails here, naming the field.
func TestBookFileFieldClasses_EveryFieldClassified(t *testing.T) {
	typ := reflect.TypeFor[BookFile]()
	fields := make(map[string]bool, typ.NumField())
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		fields[name] = true
		if _, ok := bookFileFieldClasses[name]; !ok {
			t.Errorf("BookFile.%s is not classified in bookFileFieldClasses (internal/database/bookfile_merge.go): "+
				"decide whether an upsert with a zero value must keep the stored value", name)
		}
	}
	for name := range bookFileFieldClasses {
		if !fields[name] {
			t.Errorf("bookFileFieldClasses names %q, which is not a BookFile field", name)
		}
	}
}

// scannerAlwaysSupplies names preserve-on-upsert fields the scanner-shaped row
// sets unconditionally and non-zero. Their class keeps a stored value only
// against a ZERO incoming value, so for a scanner row they are overwritten, and
// the survival check excludes them. A new field the scanner always sets goes
// here AND in rescanNotCompared.
var scannerAlwaysSupplies = map[string]bool{
	"OriginalFilename": true,
	"Format":           true,
	"FileSize":         true,
}

// The test's independent exclusion list and the production table must agree on
// which fields a rescan may change, or one of them is wrong.
func TestBookFileFieldClasses_AgreeWithRescanContract(t *testing.T) {
	for name, rule := range bookFileFieldClasses {
		class := rule.class
		_, excluded := rescanNotCompared[name]
		// bfScanObserved (Missing) is preserved by BatchUpsertBookFiles, which is
		// what the survival tests drive; the scanner's own path is tested below.
		preserved := class == bfPreserveOnUpsert || class == bfPreserveAlways || class == bfScanObserved || class == bfWriteOnce
		switch {
		case preserved && excluded && !scannerAlwaysSupplies[name]:
			t.Errorf("%s is preserved by the store but excluded from the rescan survival check "+
				"(if the scanner always supplies it, add it to scannerAlwaysSupplies)", name)
		case !preserved && !excluded:
			t.Errorf("%s is not preserved by the store (class %d) but the rescan contract requires it to survive", name, class)
		}
	}
}

// rescanNotCompared lists the BookFile fields this test deliberately does NOT
// assert survive a rescan, each with the reason. Everything else must survive.
// Kept local to the test (rather than read from the production classification
// table) so this test is an independent specification of the rescan contract.
var rescanNotCompared = map[string]string{
	"ID":        "identity: the store keeps the stored ID; the bare row's fresh ULID is discarded",
	"BookID":    "identity: the store keeps the stored BookID",
	"CreatedAt": "identity: the store keeps the stored CreatedAt",
	"UpdatedAt": "bumped on every write",
	// Dropped by marshalBookFileDropSegs on every write, so never persisted.
	"AcoustIDSeg0": "not stored", "AcoustIDSeg1": "not stored", "AcoustIDSeg2": "not stored",
	"AcoustIDSeg3": "not stored", "AcoustIDSeg4": "not stored", "AcoustIDSeg5": "not stored",
	"AcoustIDSeg6": "not stored",
	// Scanner-owned: the rescan writes these as observed, zero included.
	"FilePath":    "scanner-owned (the match key)",
	"TrackNumber": "scanner-owned (asserted separately below)",
	"DiscNumber":  "scanner-owned (asserted separately below)",
	"DiscCount":   "scanner-owned (asserted separately below)",
	// Supplied non-zero by the scanner-shaped row below, so it legitimately changes.
	"OriginalFilename": "supplied by the rescan row",
	"Format":           "supplied by the rescan row",
	"FileSize":         "supplied by the rescan row",
}

// scannerShapedRow reproduces the literal createBookFilesForBook builds
// (internal/scanner/scanner.go) for an untagged, unhashable file: only the fields
// the scanner observes are set.
func scannerShapedRow(bookID, path string) *BookFile {
	return &BookFile{
		ID:               "01SCANNERFRESHULID000000000",
		BookID:           bookID,
		FilePath:         path,
		OriginalFilename: "rescanned.m4b",
		Format:           "m4b",
		FileSize:         4242,
		TrackNumber:      3,
	}
}

func seedFullyPopulatedBookFile(t *testing.T, s *PebbleStore) (*BookFile, string) {
	t.Helper()
	book, err := s.CreateBook(&Book{Title: "Rescan Preserve"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	seed := &BookFile{}
	fillBookFileNonZero(t, seed)
	seed.ID = ""
	seed.BookID = book.ID
	seed.FilePath = "/lib/Rescan Preserve/01.m4b"
	// A known-kind original: the filler's arbitrary kind string would read as a
	// legacy value, which the merge lets a scan replace.
	seed.OriginalFileHashKind = FileHashKindSampled
	if err := s.CreateBookFile(seed); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	stored, err := s.GetBookFiles(book.ID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("GetBookFiles after seed: err=%v len=%d", err, len(stored))
	}
	// Sanity: every field we intend to compare really was stored non-zero, or a
	// "survived" assertion would pass vacuously.
	sv := reflect.ValueOf(stored[0])
	for i := range sv.NumField() {
		name := sv.Type().Field(i).Name
		if _, skip := rescanNotCompared[name]; skip {
			continue
		}
		if sv.Field(i).IsZero() {
			t.Fatalf("seed field %s was not stored non-zero; the survival check would be vacuous", name)
		}
	}
	return &stored[0], book.ID
}

func assertRescanPreserved(t *testing.T, before, after *BookFile) {
	t.Helper()
	bv, av := reflect.ValueOf(*before), reflect.ValueOf(*after)
	var wiped []string
	for i := range bv.NumField() {
		name := bv.Type().Field(i).Name
		if _, skip := rescanNotCompared[name]; skip {
			continue
		}
		if !reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			wiped = append(wiped, name)
		}
	}
	if len(wiped) > 0 {
		t.Errorf("a scanner-shaped rescan changed %d field(s) it does not own: %v", len(wiped), wiped)
	}
	if after.ID != before.ID || after.BookID != before.BookID || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("identity not kept: got ID=%s BookID=%s CreatedAt=%v, want %s %s %v",
			after.ID, after.BookID, after.CreatedAt, before.ID, before.BookID, before.CreatedAt)
	}
	// Scanner-owned position fields: the rescan's zero disc is authoritative (a
	// positional fallback clears a disc placement; see metadata.ApplyTagPlacement).
	if after.TrackNumber != 3 || after.DiscNumber != 0 || after.DiscCount != 0 {
		t.Errorf("scanner-owned position not overwritten: track=%d disc=%d discCount=%d, want 3 0 0",
			after.TrackNumber, after.DiscNumber, after.DiscCount)
	}
	if after.FileSize != 4242 || after.Format != "m4b" || after.OriginalFilename != "rescanned.m4b" {
		t.Errorf("rescan-supplied fields not written: size=%d format=%q orig=%q",
			after.FileSize, after.Format, after.OriginalFilename)
	}
}

// A library rescan hands BatchUpsertBookFiles a bare row carrying only what the
// scanner observed. Every field it does not own — transcription state, duration,
// media info, hashes, Missing/SkipScan, fingerprint state, iTunes and Deluge
// provenance — must keep its stored value.
func TestBatchUpsertBookFiles_ScannerRescanPreservesUnownedFields(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	before, bookID := seedFullyPopulatedBookFile(t, s)
	if err := s.BatchUpsertBookFiles([]*BookFile{scannerShapedRow(bookID, before.FilePath)}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	after, err := s.GetBookFiles(bookID)
	if err != nil || len(after) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(after))
	}
	assertRescanPreserved(t, before, &after[0])
}

// UpsertBookFile is the single-row twin and must apply the same rule.
func TestUpsertBookFile_ScannerRescanPreservesUnownedFields(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	before, bookID := seedFullyPopulatedBookFile(t, s)
	if err := s.UpsertBookFile(scannerShapedRow(bookID, before.FilePath)); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}
	after, err := s.GetBookFiles(bookID)
	if err != nil || len(after) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(after))
	}
	assertRescanPreserved(t, before, &after[0])
}

// UpdateBookFile is the full-row write and the documented way to clear a field
// on purpose (repoint, mark-missing, fingerprint retry; the PATCH handler uses
// PatchBookFileFields, a field-level setter outside the merge). A
// read-modify-write that zeroes a field must land as written — except the two
// memdb-stripped heavy fields, which a slim round-trip cannot carry.
func TestUpdateBookFile_IntentionalClearsLand(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	before, bookID := seedFullyPopulatedBookFile(t, s)
	row := *before
	row.Missing = false
	row.SkipScan = false
	row.DownloadHash = ""
	row.TranscribeStatus = nil
	row.TranscribeError = nil
	row.FingerprintFailedAt = nil
	row.FingerprintFailureReason = nil
	row.Duration = 0
	row.AcoustIDFingerprint = nil // memdb-stripped: must be restored
	row.IntroTranscription = nil  // memdb-stripped: must be restored
	if err := s.UpdateBookFile(row.ID, &row); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	after, err := s.GetBookFiles(bookID)
	if err != nil || len(after) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(after))
	}
	got := after[0]
	for name, cleared := range map[string]bool{
		"Missing":                  !got.Missing,
		"SkipScan":                 !got.SkipScan,
		"DownloadHash":             got.DownloadHash == "",
		"TranscribeStatus":         got.TranscribeStatus == nil,
		"TranscribeError":          got.TranscribeError == nil,
		"FingerprintFailedAt":      got.FingerprintFailedAt == nil,
		"FingerprintFailureReason": got.FingerprintFailureReason == nil,
		"Duration":                 got.Duration == 0,
	} {
		if !cleared {
			t.Errorf("UpdateBookFile did not apply the intentional clear of %s", name)
		}
	}
	if fmt.Sprint(got.AcoustIDFingerprint) != fmt.Sprint(before.AcoustIDFingerprint) {
		t.Errorf("AcoustIDFingerprint wiped by a slim round-trip: %v", got.AcoustIDFingerprint)
	}
	if got.IntroTranscription == nil || *got.IntroTranscription != *before.IntroTranscription {
		t.Errorf("IntroTranscription wiped by a slim round-trip: %v", got.IntroTranscription)
	}
}
