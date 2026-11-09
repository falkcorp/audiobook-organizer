// file: cmd/cli_store_test.go
// version: 1.0.0
// guid: 0c0b5a54-1d2f-4a49-9a8e-7e6a3f1b2c40
// last-edited: 2026-11-09

package cmd

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/remux"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// isolateCLIStore points the loaded config and the secure state directory at
// a temporary tree, as a CLI run names them, and returns the state directory
// and the (not yet created) store path.
func isolateCLIStore(t *testing.T) (stateDir, dbPath string) {
	t.Helper()
	stateDir = isolateStateDir(t)
	dbPath = filepath.Join(t.TempDir(), "db", "books.pebble")
	orig := config.AppConfig
	config.AppConfig.DatabaseType, config.AppConfig.DatabasePath = "pebble", dbPath
	t.Cleanup(func() { config.AppConfig = orig })
	return stateDir, dbPath
}

// isolateStateDir points the secure state directory (where the swap slot
// is) at a fresh temporary directory for the test. The directory is
// memoised process-wide (config.EnsureSecureStateDir), so the memo is reset
// on the way in and out: a later test must not probe this one's.
func isolateStateDir(t *testing.T) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv(config.SecureStateDirEnv, stateDir)
	config.ResetStateDirForTest()
	t.Cleanup(config.ResetStateDirForTest)
	return stateDir
}

// seedStarted makes the store at dbPath one the service has started once:
// the standalone opener runs core's Prepare and Migrate over it, which is
// what a first serve boot leaves behind.
func seedStarted(t *testing.T, dbPath string) {
	t.Helper()
	s, err := database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close the seeded store: %v", err)
	}
}

// stageSwap stages an archive restore in the one swap slot under stateDir,
// as `storage restore-offline` does, so the next start of the service
// applies it.
func stageSwap(t *testing.T, stateDir, dbPath string) {
	t.Helper()
	const id = "20261108T000000Z-batcave2"
	err := restorejournal.CreateIntent(stateDir, restorejournal.Intent{
		ID:     id,
		Kind:   restorejournal.KindArchive,
		DBPath: dbPath,
		Targets: []restorejournal.Target{{
			From: filepath.Join(restorejournal.StagingDir(dbPath, id), "db"),
			To:   dbPath,
		}},
	})
	if err != nil {
		t.Fatalf("stage the swap: %v", err)
	}
}

// useRealOpener makes the CLI open the store the way a user's run does,
// whatever an earlier test stubbed (stubCommandDeps restores it, but a test
// in this file must not depend on running after or before it).
func useRealOpener(t *testing.T) {
	t.Helper()
	orig := openCLIStore
	openCLIStore = openOfflineStore
	t.Cleanup(func() { openCLIStore = orig })
}

// treeSnapshot is every file under dir with its size and modification time,
// to show that a refused command did not touch the store: a Pebble open
// adds or rewrites at least its LOCK, WAL or MANIFEST.
func treeSnapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel+" "+strconv.FormatInt(fi.Size(), 10)+" "+strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}

// cliRefs is every selector expression of cmd's non-test sources that names
// an exported member of an imported package, with the file (slash path under
// cmd/), the top-level declaration it sits in (function, method or
// package-level var, const or type name) and the package's import path.
type cliRef struct {
	file, decl, importPath, name string
}

func (r cliRef) selector() string {
	return path.Base(strings.TrimSuffix(r.importPath, "/v2")) + "." + r.name
}

// scanCLIRefs parses every non-test .go file under cmd/ directly, so a file
// behind the bench build tag is read like any other (go/parser ignores build
// constraints), and returns its package-qualified selector references plus
// the identifiers each file uses. A local variable that shadows an import's
// name is not a package reference (its Obj is set).
func scanCLIRefs(t *testing.T) (refs []cliRef, files []string, idents map[string]map[string]bool) {
	t.Helper()
	idents = map[string]map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(p)
		files = append(files, name)
		idents[name] = map[string]bool{}
		imports := map[string]string{} // local name -> import path
		for _, im := range file.Imports {
			ip, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				return err
			}
			local := path.Base(ip)
			if len(local) >= 2 && local[0] == 'v' && strings.Trim(local[1:], "0123456789") == "" {
				local = path.Base(path.Dir(ip)) // .../pebble/v2 is package pebble
			}
			if im.Name != nil {
				local = im.Name.Name
			}
			imports[local] = ip
		}
		visit := func(decl string, n ast.Node) {
			ast.Inspect(n, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					idents[name][x.Name] = true
				case *ast.SelectorExpr:
					id, ok := x.X.(*ast.Ident)
					if !ok || id.Obj != nil {
						return true
					}
					if ip, ok := imports[id.Name]; ok {
						refs = append(refs, cliRef{file: name, decl: decl, importPath: ip, name: x.Sel.Name})
					}
				}
				return true
			})
		}
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				visit(d.Name.Name, d)
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					switch sp := sp.(type) {
					case *ast.ValueSpec:
						visit(sp.Names[0].Name, sp)
					case *ast.TypeSpec:
						visit(sp.Name.Name, sp)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan cmd/: %v", err)
	}
	return refs, files, idents
}

const (
	aboDatabase  = "github.com/falkcorp/audiobook-organizer/internal/database"
	pebblePkg    = "github.com/cockroachdb/pebble/v2"
	mediacoreApp = "github.com/falkcorp/mediacore/app"
)

// TestCLISubcommandsUseOfflineOpen (06 §6.4.6; 05-storage §5S.4; R1-25): every
// ABO CLI subcommand that reads or writes the library store opens it through
// app.OpenOffline, the guard-aware offline open, and none opens the store
// itself.
//
// Part A is a source scan: a reference to any way of opening the store
// outside core's offline open, anywhere under cmd/ (bench-tagged files and
// the small tools included), must be on the allowlist below with its reason,
// and an allowlist entry that matches nothing fails, so the list cannot go
// stale.
//
// Part B runs the real opener: a staged swap, a store the service never
// started, and the open that works, through the commands themselves.
func TestCLISubcommandsUseOfflineOpen(t *testing.T) {
	t.Run("source scan", func(t *testing.T) {
		refs, files, idents := scanCLIRefs(t)

		banned := map[string][]string{
			aboDatabase: {"InitializeStore", "InitializeStoreWith", "NewPebbleStore", "NewPebbleStoreInMemory",
				"OpenStandalone", "ListReservedPreferences", "RepairReservedPreference"},
			pebblePkg: {"Open"},
		}
		// file|declaration|selector -> why it may open the store itself.
		allow := map[string]string{
			"child_mode.go|initializeChildStore|database.InitializeStoreWith": "the op-runner child is not an offline subcommand: " +
				"the serving parent re-execs it over the live store, which app.OpenOffline refuses; it keeps the standalone " +
				"opener and passes StateDir so the opener reads the swap slot first",
			"diagnostics.go|runRawPebbleQuery|pebble.Open": "diagnostics query --raw inspects a store the guard refuses, by design; " +
				"it probes the swap slot first (refuseBusySwapSlot)",
			"diagnostics.go|runReservedPrefs|database.ListReservedPreferences": "reserved-prefs is a recovery tool that bypasses the guard by design; " +
				"it probes the swap slot first, in every mode (refuseBusySwapSlot)",
			"diagnostics.go|repairReservedPreference|database.RepairReservedPreference": "the same recovery tool's --set and --delete; " +
				"a var only so a test can fail it after a commit",
			"pid-census/main.go|main|database.NewPebbleStore": "pid-census opens a COPY of the store, never the live one",
		}

		used := map[string]bool{}
		for _, r := range refs {
			if !slices.Contains(banned[r.importPath], r.name) {
				continue
			}
			key := r.file + "|" + r.decl + "|" + r.selector()
			if _, ok := allow[key]; !ok {
				t.Errorf("%s (in %s) opens the store itself: open it with openCLIStore (app.OpenOffline) or add it to the allowlist with a reason", r.selector(), r.file+" "+r.decl)
				continue
			}
			used[key] = true
		}
		for key := range allow {
			if !used[key] {
				t.Errorf("allowlist entry %q matches no reference: remove it", key)
			}
		}

		// The opener really is core's offline open.
		if !slices.Contains(refs, cliRef{file: "cli_store.go", decl: "openOfflineStore", importPath: mediacoreApp, name: "OpenOffline"}) {
			t.Error("openOfflineStore does not call app.OpenOffline")
		}
		// The scan reached the bench-tagged command and the nested tools.
		for _, f := range []string{"dedup_bench.go", "pid-census/main.go"} {
			if !slices.Contains(files, f) {
				t.Errorf("the scan did not read %s", f)
			}
		}
		// Each file that opens a store for a subcommand does it through openCLIStore.
		for _, f := range []string{"root.go", "seed.go", "diagnostics.go", "dedup_bench.go", "transcode_skip.go"} {
			if !idents[f]["openCLIStore"] {
				t.Errorf("%s never calls openCLIStore", f)
			}
		}
		// The raw-open paths probe the swap slot.
		for _, fn := range []string{"runRawPebbleQuery", "runReservedPrefs"} {
			if !slices.ContainsFunc(refs, func(r cliRef) bool { return r.decl == fn && r.file == "diagnostics.go" }) {
				t.Fatalf("diagnostics.go has no %s", fn)
			}
		}
	})

	t.Run("staged swap refuses every store command", func(t *testing.T) {
		useRealOpener(t)
		stateDir, dbPath := isolateCLIStore(t)
		seedStarted(t, dbPath) // a valid store: only the swap is wrong
		stageSwap(t, stateDir, dbPath)
		before := treeSnapshot(t, dbPath)

		config.AppConfig.RootDir = t.TempDir()
		var ran []string
		origScan, origProcess, origPlaylists, origTags := scanDirectory, processBooks, generatePlaylists, updateSeriesTags
		t.Cleanup(func() {
			scanDirectory, processBooks, generatePlaylists, updateSeriesTags = origScan, origProcess, origPlaylists, origTags
		})
		scanDirectory = func(context.Context, string, logger.Logger) ([]scanner.Book, error) {
			ran = append(ran, "scanDirectory")
			return nil, nil
		}
		processBooks = func([]scanner.Book, logger.Logger) error { ran = append(ran, "processBooks"); return nil }
		generatePlaylists = func() error { ran = append(ran, "generatePlaylists"); return nil }
		updateSeriesTags = func() error { ran = append(ran, "updateSeriesTags"); return nil }
		origSeed := [3]int{seedCount, seedAuthors, seedSeries}
		seedCount, seedAuthors, seedSeries = 2, 1, 1
		t.Cleanup(func() { seedCount, seedAuthors, seedSeries = origSeed[0], origSeed[1], origSeed[2] })

		for name, run := range map[string]func() error{
			"scan":                      func() error { return scanCmd.RunE(scanCmd, nil) },
			"playlist":                  func() error { return playlistCmd.RunE(playlistCmd, nil) },
			"tag":                       func() error { return tagCmd.RunE(tagCmd, nil) },
			"organize":                  func() error { return organizeCmd.RunE(organizeCmd, nil) },
			"seed":                      func() error { return runSeed(seedCmd, nil) },
			"transcode-skip":            func() error { return runTranscodeSkip(transcodeSkipCmd, []string{"/library/Hush.m4b"}) },
			"diagnostics cleanup":       func() error { return runCleanupInvalidBooks(true, false) },
			"diagnostics query":         func() error { return runDiagnosticsQuery(1, "book:", false) },
			"diagnostics query --raw":   func() error { return runDiagnosticsQuery(1, "book:", true) },
			"diagnostics reserved list": func() error { return runReservedPrefs(dbPath, "", "", false, false, nil) },
			"diagnostics reserved set": func() error {
				return runReservedPrefs(dbPath, "", "storage_format=2", true, false, nil)
			},
			"diagnostics reserved delete": func() error {
				return runReservedPrefs(dbPath, "storage_format", "", true, false, nil)
			},
		} {
			t.Run(name, func(t *testing.T) {
				ran = nil
				err := run()
				if !errors.Is(err, restorejournal.ErrSwapPending) {
					t.Fatalf("err = %v, want restorejournal.ErrSwapPending", err)
				}
				if len(ran) > 0 {
					t.Fatalf("the command ran %v after the open was refused", ran)
				}
			})
		}
		if after := treeSnapshot(t, dbPath); !slices.Equal(before, after) {
			t.Errorf("a refused command changed the store directory:\nbefore %v\nafter  %v", before, after)
		}
		if _, err := os.Stat(restorejournal.PendingPath(stateDir)); err != nil {
			t.Errorf("the staged swap is gone: %v", err)
		}
		if database.GetGlobalStore() != nil {
			t.Error("a refused open left a global store")
		}
	})

	t.Run("fresh install is refused and nothing is created", func(t *testing.T) {
		useRealOpener(t)
		_, dbPath := isolateCLIStore(t)
		config.AppConfig.RootDir = t.TempDir()
		scanned := false
		origScan := scanDirectory
		t.Cleanup(func() { scanDirectory = origScan })
		scanDirectory = func(context.Context, string, logger.Logger) ([]scanner.Book, error) {
			scanned = true
			return nil, nil
		}
		origSeed := [3]int{seedCount, seedAuthors, seedSeries}
		seedCount, seedAuthors, seedSeries = 2, 1, 1
		t.Cleanup(func() { seedCount, seedAuthors, seedSeries = origSeed[0], origSeed[1], origSeed[2] })

		for name, run := range map[string]func() error{
			"scan":                func() error { return scanCmd.RunE(scanCmd, nil) },
			"seed":                func() error { return runSeed(seedCmd, nil) },
			"transcode-skip":      func() error { return runTranscodeSkip(transcodeSkipCmd, []string{"/library/Hush.m4b"}) },
			"diagnostics cleanup": func() error { return runCleanupInvalidBooks(true, false) },
			"diagnostics query":   func() error { return runDiagnosticsQuery(1, "book:", false) },
		} {
			t.Run(name, func(t *testing.T) {
				err := run()
				if !errors.Is(err, storage.ErrNoStore) {
					t.Fatalf("err = %v, want storage.ErrNoStore", err)
				}
				if !strings.Contains(err.Error(), "start the service once") {
					t.Errorf("err = %q, want it to tell the user to start the service once", err)
				}
				if scanned {
					t.Error("scan ran after the open was refused")
				}
				if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the refused open created the store (%v)", err)
				}
			})
		}
		// seed names its own fix.
		if err := runSeed(seedCmd, nil); err == nil || !strings.Contains(err.Error(), "run seed again") {
			t.Errorf("seed on a fresh install = %v, want the start-the-service-then-seed message", err)
		}
	})

	t.Run("a started store opens read-write and read-only", func(t *testing.T) {
		_, dbPath := isolateCLIStore(t)
		seedStarted(t, dbPath)

		store, closer, err := openOfflineStore(context.Background(), false)
		if err != nil {
			t.Fatalf("read-write open: %v", err)
		}
		if database.GetGlobalStore() == nil {
			t.Error("the open did not register the global store")
		}
		if _, err := store.CreateAuthor("Selina Kyle"); err != nil {
			t.Fatalf("write through the offline open: %v", err)
		}
		closer()
		if database.GetGlobalStore() != nil {
			t.Error("the closer left the global store registered")
		}

		store, closer, err = openOfflineStore(context.Background(), true)
		if err != nil {
			t.Fatalf("read-only open: %v", err)
		}
		defer closer()
		got, err := store.GetAuthorByName("Selina Kyle")
		if err != nil || got == nil || got.Name != "Selina Kyle" {
			t.Fatalf("the write did not land: %+v, %v", got, err)
		}
	})

	t.Run("seed writes through the offline open", func(t *testing.T) {
		useRealOpener(t)
		_, dbPath := isolateCLIStore(t)
		seedStarted(t, dbPath)
		config.AppConfig.RootDir = t.TempDir()
		origSeed := [3]int{seedCount, seedAuthors, seedSeries}
		seedCount, seedAuthors, seedSeries = 2, 1, 1
		t.Cleanup(func() { seedCount, seedAuthors, seedSeries = origSeed[0], origSeed[1], origSeed[2] })
		var out bytes.Buffer
		seedCmd.SetOut(&out)
		t.Cleanup(func() { seedCmd.SetOut(nil) })

		if err := runSeed(seedCmd, nil); err != nil {
			t.Fatalf("seed: %v\n%s", err, out.String())
		}
		store, closer, err := openOfflineStore(context.Background(), true)
		if err != nil {
			t.Fatalf("read-only open: %v", err)
		}
		defer closer()
		books, err := store.GetAllBooksCore(10, 0)
		if err != nil || len(books) != 2 {
			t.Fatalf("books after seed --count 2 = %d, %v", len(books), err)
		}
	})

	t.Run("transcode-skip writes through the offline open", func(t *testing.T) {
		useRealOpener(t)
		_, dbPath := isolateCLIStore(t)
		seedStarted(t, dbPath)
		var out bytes.Buffer
		transcodeSkipCmd.SetOut(&out)
		t.Cleanup(func() { transcodeSkipCmd.SetOut(nil) })
		files := []string{"/library/Hush.m4b", "/library/The Long Halloween.m4b"}

		if err := runTranscodeSkip(transcodeSkipCmd, files); err != nil {
			t.Fatalf("transcode-skip: %v\n%s", err, out.String())
		}
		store, closer, err := openOfflineStore(context.Background(), true)
		if err != nil {
			t.Fatalf("read-only open: %v", err)
		}
		defer closer()
		for _, f := range files {
			key := remux.TranscodeSkipKey(f)
			s, err := store.GetSetting(key)
			if err != nil || s == nil || s.Value != "true" || s.Type != "bool" || s.IsSecret {
				t.Fatalf("setting %s for %s = %+v, %v; want \"true\" (bool, not secret)", key, f, s, err)
			}
			if !strings.Contains(out.String(), key) {
				t.Errorf("the output does not name %s:\n%s", key, out.String())
			}
		}
	})

	t.Run("raw and reserved-prefs refuse before any Pebble open", func(t *testing.T) {
		stateDir, dbPath := isolateCLIStore(t)
		seedStarted(t, dbPath)
		stageSwap(t, stateDir, dbPath)
		before := treeSnapshot(t, dbPath)

		for name, err := range map[string]error{
			"raw":             runRawPebbleQuery(1, "book:"),
			"reserved list":   runReservedPrefs(dbPath, "", "", false, false, nil),
			"reserved set":    runReservedPrefs(dbPath, "", "db_version=3", true, false, nil),
			"reserved delete": runReservedPrefs(dbPath, "storage_format", "", true, false, nil),
		} {
			if !errors.Is(err, restorejournal.ErrSwapPending) {
				t.Errorf("%s: err = %v, want restorejournal.ErrSwapPending", name, err)
			}
		}
		if after := treeSnapshot(t, dbPath); !slices.Equal(before, after) {
			t.Errorf("a refused command changed the store directory:\nbefore %v\nafter  %v", before, after)
		}
	})
}
