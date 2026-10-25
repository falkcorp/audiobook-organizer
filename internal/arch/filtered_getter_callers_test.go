// file: internal/arch/filtered_getter_callers_test.go
// version: 1.0.1
// guid: 6c2f9d04-1b8e-4a73-85d6-e0a47b31c9f2
// last-edited: 2026-10-25

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// filteredGetters are the book_file / book readers that answer LIVE rows only: a
// row (or segment hash) held only by a retired version answers nil, so a nil is
// "no LIVE owner", never "free" (internal/database/iface_retired.go, THE ONE
// RULE). A caller that treats nil as "nothing holds this" before writing a row
// on the identity, importing a file as new, or releasing a file is a bug the
// type system cannot see: the complete lookups are BookFilesAtPath /
// BookFileRowsAtPathForWrite (paths), BookFilesHoldingHash / RetiredHoldersOfHash
// (hashes) and the *IncludingRetired readers.
var filteredGetters = map[string]bool{
	"GetBookFileByPath":          true,
	"GetBookFileByPID":           true,
	"GetBookFileByAcoustID":      true,
	"GetBookFileByAcoustIDFuzzy": true,
	"GetBookBySegmentFileHash":   true,
}

// reviewedFilteredGetterCallers is every non-test function that calls a filtered
// getter, each with why a live-only answer is right there. It is a ratchet in
// both directions: a NEW caller fails TestFilteredGetterCallers_AreAllReviewed
// until it is listed (decide, and write down, whether nil can mean "free" at that
// site, and if it can, use the complete lookup instead), and a listed site that
// no longer calls the getter fails it too, so the list cannot go stale.
//
// Key: "<file>|<enclosing function>|<getter>", file relative to the module root.
var reviewedFilteredGetterCallers = map[string]string{
	// --- Live-only is the intent: nil means "no LIVE row", and that is the question.
	"internal/dedup/collectors_acoustid.go|CollectExactAcoustID|GetBookFileByAcoustID":      liveByIntent + "Dedup candidates are pairs of live books; a fingerprint held only by a retired version must not nominate its holder.",
	"internal/dedup/engine.go|Engine.AcoustIDScan|GetBookFileByAcoustID":                    liveByIntent + "Same as CollectExactAcoustID: the exact tier pairs live books only.",
	"internal/reconcile/itunes_heal.go|resolveAmbiguousByAcoustID|GetBookFileByPath":        liveByIntent + "Heal repairs live rows; a path with no live row returns \"\", 0 (no resolution), the safe direction.",
	"internal/reconcile/itunes_heal.go|resolveAmbiguousByBookMeta|GetBookFileByPath":        liveByIntent + "A candidate path with no live row is skipped (continue); nothing is written on the answer.",
	"internal/reconcile/itunes_heal.go|resolveAmbiguousByDB|GetBookFileByPath":              liveByIntent + "A candidate path with no live row is skipped; the answer only ranks candidates.",
	"internal/reconcile/itunes_heal.go|resolveAmbiguousByTranscription|GetBookFileByPath":   liveByIntent + "nil falls back to fingerprinting the file afresh; read-only.",
	"internal/reconcile/itunes_heal.go|resolveNotFoundByPID|GetBookFileByPID":               liveByIntent + "nil returns \"\" (not resolved) and the PID is left for the next pass; read-only.",
	"internal/plugins/maintenance/repoint_book_file_rows.go|rbfCheckDisk|GetBookFileByPath": "A second opinion AFTER the complete owner scan above it (AllBookFilesCoreIncludingRetired, retired rows included); it can only add an owner, never clear one.",
	"internal/scanner/replaced_file_probe.go|storedBookFileAt|GetBookFileByPath":            liveByIntent + "nil only skips the replaced-file probe (the safe direction); the upsert it mirrors matches live rows only (retired rows are never an upsert match).",

	// --- Inside internal/database: the getter's own plumbing and live-row writers.
	"internal/database/book_file_owners.go|BookFileRowsAtPath|GetBookFileByPath":                                    "The documented single-row fallback of the scanner variant when the complete lookup is unavailable; callers that gate a WRITE use BookFileRowsAtPathForWrite / BookFileRowsAtPathStrict instead.",
	"internal/database/pebble_store_bookfiles.go|PebbleStore.ClearITunesPID|GetBookFileByPID":                       liveByIntent + "Clears the PID on the LIVE row that carries it; a retired row keeps its PID by design (the retire hands the key to a live row when there is one).",
	"internal/database/pebble_store_bookfiles.go|PebbleStore.GetBookFileByAcoustIDFuzzy|GetBookFileByAcoustIDFuzzy": "The store's own delegation to the memdb implementation of the same live-only reader.",
	"internal/database/pebble_store_mark_import.go|PebbleStore.MarkFileImportedFromDeluge|GetBookFileByPath":        liveByIntent + "Stamps the Deluge import on the live row at the path; with no live row it falls through to the torrent-hash match or a documented no-op, and no row is created from nil.",
	"internal/database/pebble_store_scancache.go|PebbleStore.UpdateBookFileScanCache|GetBookFileByPath":             liveByIntent + "Stamps mtime/size on the live row at the path; a retired row is not scanned, and no row is created from nil.",
	"internal/database/retired_mock_store.go|MockStore.GetBookFileByPIDIncludingRetired|GetBookFileByPID":           "MockStore holds no retired rows, so its live reader is its including-retired reader.",
	"internal/database/retired_mock_store.go|MockStore.GetBookFileByPathIncludingRetired|GetBookFileByPath":         "MockStore holds no retired rows, so its live reader is its including-retired reader.",
	"pkg/ops/opstest/fakes.go|FakeBooks.GetBookFileByPath|GetBookFileByPath":                                        "Test fake delegating to its own in-memory store.",
	"pkg/ops/opstest/store.go|worldStore.GetBookFileByPath|GetBookFileByPath":                                       "Test fake delegating to its own in-memory store.",

	// --- Deliberately live after the retired check.

	// --- Known soft-fail.
	"internal/deluge/discovery.go|IsContentFingerprintTracked|GetBookFileByAcoustID":      "KNOWN SOFT-FAIL: nil reads as \"content not tracked\", so a torrent whose content a retired version holds is offered for import by discovery (the import itself is refused by the scanner's retired-content check). Open item; replace with a complete fingerprint lookup.",
	"internal/deluge/discovery.go|IsContentFingerprintTracked|GetBookFileByAcoustIDFuzzy": "KNOWN SOFT-FAIL: same as the exact lookup above.",

	// --- Converted to the IncludingRetired readers by #4139.
	"internal/deluge/import.go|ImportToLibraryWith|GetBookFileByPath":                         pending4139,
	"internal/deluge/importer_adapter.go|LibraryImporterAdapter.ImportPath|GetBookFileByPath": pending4139,
	"internal/fileops/book_file_hash_recorder.go|HashOptionsForPath|GetBookFileByPath":        pending4139,
	"internal/itunes/service/importer.go|Importer.bookIDsByTrackPIDs|GetBookFileByPID":        pending4139,
	"internal/operations/opswriter/writer.go|Writer.RepointBookFile|GetBookFileByPath":        pending4139,
	"internal/organizer/collision.go|resolveTargetCollisions|GetBookFileByPath":               pending4139,
	"internal/remux/transcode.go|Transcoder.transcodeAndRecord|GetBookFileByPath":             pending4139,
	"internal/repairs/writer_files.go|Writer.RepointBookFile|GetBookFileByPath":               pending4139,
	"internal/scanner/scan_book_lock.go|resolveScanLockSet|GetBookFileByPath":                 pending4139,
	"internal/scanner/scan_existing.go|scanExisting.lookupSegments|GetBookBySegmentFileHash":  pending4139,
}

const liveByIntent = "Live-only by intent. "

const pending4139 = "PENDING #4139 (retired-versions callers): still the live-only reader on this branch, so a path or identity held only by a retired version answers nil here. #4139 converts the site to the IncludingRetired reader; this entry then fails as stale, so delete it in that rebase."

// scanFilteredGetterCallers parses every non-test Go file of the module and
// returns the set of "<file>|<function>|<getter>" call sites. The generated
// mocks are skipped: they define the getters, they do not decide on them.
func scanFilteredGetterCallers(t *testing.T) map[string]bool {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(wd, "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found from %s: %v", wd, err)
	}
	found := map[string]bool{}
	files := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "vendor", "web", ".standards", "mocks":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		files++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				name = receiverName(fn.Recv.List[0].Type) + "." + name
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && filteredGetters[sel.Sel.Name] {
					found[rel+"|"+name+"|"+sel.Sel.Name] = true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A scan that read nothing would pass vacuously.
	if files < 500 {
		t.Fatalf("scanned only %d Go files; the walk is broken", files)
	}
	return found
}

func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return receiverName(x.X)
	}
	return "?"
}

func TestFilteredGetterCallers_AreAllReviewed(t *testing.T) {
	found := scanFilteredGetterCallers(t)
	var unreviewed, stale []string
	for k := range found {
		if _, ok := reviewedFilteredGetterCallers[k]; !ok {
			unreviewed = append(unreviewed, k)
		}
	}
	for k := range reviewedFilteredGetterCallers {
		if !found[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(unreviewed)
	sort.Strings(stale)
	for _, k := range unreviewed {
		t.Errorf("unreviewed caller of a live-only getter: %s\n\tdecide whether a nil answer can mean \"free\" there (a retired version's file/identity answers nil); if it can, use the complete lookup (see filteredGetters), then add the site to reviewedFilteredGetterCallers with the reason", k)
	}
	for _, k := range stale {
		t.Errorf("reviewedFilteredGetterCallers lists a site that no longer calls the getter: %s (delete the entry)", k)
	}
	for k, why := range reviewedFilteredGetterCallers {
		if strings.TrimSpace(why) == "" {
			t.Errorf("reviewedFilteredGetterCallers[%s] has no reason", k)
		}
	}
}
