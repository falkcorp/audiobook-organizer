// file: core/internal/archtest_layers_test.go
// version: 1.0.18
// guid: 2d3bc73d-ccb4-47c7-92d8-0d5c18bcfb8a
// last-edited: 2026-11-12

// Package internal holds core's R4 layer guard (03 §3.5.1 item 2), the
// redesigned successor of ABO's internal/arch/layering_test.go for core. It
// has no non-test code.
package internal

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

const corePath = "github.com/falkcorp/mediacore"

// coreLayerOf is 03 R4's whole layer table, deferred packages included, so a
// later row that creates a package R4 names edits no guard file. L0 is 0 and
// L9 is 9. A public sub-package p/q takes p's layer unless the table names it
// (coreFamily). The test-support packages (coretest and its sub-packages,
// archtest and its sub-packages, the <pkg>/<pkg>test conformance suites) are
// not layered: they may import any core package and no core non-test package
// imports them (archtest.CoreTestSupport).
//
// R1-09, R1-36 and R1-13 edit this file only to change the in-module control (03
// §3.5.1 item 2), never this table; the exceptions are R1-13's
// internal/openapi entry, R1-20's internal/pebble entry, R1-16's
// internal/sdnotify entry, R1-21's internal/bootseam entry, F4-01's
// internal/rbacstore entry, F3-01's internal/configstore entry, F4-09's
// internal/authz entry, F4-02's internal/authstore entry and F2-04's
// internal/schedspec entry, F7-07's internal/bleveindex entry, F6-02's
// internal/sse entry and F8-01's internal/backuparchive entry, R4 packages the
// table missed.
var coreLayerOf = map[string]int{
	// L0
	"entity": 0, "lifecycle": 0, "version": 0, "buildid": 0, "log": 0, "metrics": 0,
	"storage/keys": 0, "storage/codec": 0, "storage/restorejournal": 0,
	"internal/keyedmutex": 0, "internal/jsonschemax": 0,
	// internal/schedspec is F2-04's schedule grammar (cron, daily_at, the
	// schedule key form), the standard library only, shared by tasks (config
	// time) and internal/scheduler (poll time).
	"internal/schedspec": 0,
	// internal/sdnotify (R1-16) is the systemd notify protocol over the
	// standard library; its one core import is lifecycle's constants.
	"internal/sdnotify": 0,
	// internal/bootseam is R1-21's per-boot seam of the crash-matrix kit: the
	// layer of its consumers' lowest, so storage (L1) may read it.
	"internal/bootseam": 0,
	// internal/backuparchive (F8-01) is backup's archive codecs and the hardened
	// tar reader/extractor over the standard library and klauspost/compress; it
	// imports no core package, so backup (L4) and the offline restore path may
	// both read it.
	"internal/backuparchive": 0,
	// L1 (internal/restoreswap imports only storage and its L0 leaves;
	// internal/pebble implements storage, so it takes storage's layer, R4)
	"storage": 1, "storage/index": 1, "internal/restoreswap": 1, "internal/pebble": 1,
	// L2
	"secrets": 2,
	// L3
	"config": 3,
	// L4 (internal/storagemigrate and internal/logjournal are bridges: the
	// layer of their highest import)
	"events": 4, "outbox": 4, "tasks": 4, "storage/datamigrate": 4, "library": 4,
	"auth": 4, "auth/oidc": 4, "auth/password": 4, "rbac": 4, "audit": 4,
	"integrations": 4, "backup": 4, "backup/journal": 4, "updater": 4,
	"health": 4, "health/diagnostics": 4,
	"internal/storagemigrate": 4, "internal/logjournal": 4,
	// internal/rbacstore implements rbac's role Store over c/rbac/ (F4-01,
	// 05-rbac §6.3), so it takes rbac's layer (R4).
	"internal/rbacstore": 4,
	// internal/authz is the implementation of rbac (05-rbac §3, F4-09): the
	// compiled checker and the two privileged checkers, so it takes rbac's
	// layer (R4).
	"internal/authz": 4,
	// internal/authstore implements auth's identity Store over c/auth/
	// (F4-02, 05-authn §3.6), so it takes auth's layer (R4).
	"internal/authstore": 4,
	// internal/taskstore is the c/tasks/ key layout and raw staging under
	// tasks' NativeStores and Importer (F2-02, 5T.6.1), so it takes tasks'
	// layer (R4).
	"internal/taskstore": 4,
	// internal/executor is the implementation of tasks.Executor (F2-03,
	// 05-tasks 5T.4): it imports tasks, taskstore, rbac's internal/authz,
	// health and metrics, so it takes tasks' layer (R4).
	"internal/executor": 4,
	// internal/scheduler is core's scheduler (F2-04, 05-tasks 5T.3.8): it
	// imports tasks, storage and rbac and drives the executor through
	// tasks.Stager, so it takes tasks' layer (R4).
	"internal/scheduler": 4,
	// internal/configstore (F3-01) is a bridge too: config's native override
	// store and the kernel's committer, which joins config, storage, secrets
	// and audit, its highest import.
	"internal/configstore": 4,
	// internal/instancestore is the c/integrations/ key layout and raw
	// staging under integrations' NativeInstanceStore and NativeImporter
	// (F7-01, 05-metadata M1.4), so it takes integrations' layer (R4).
	"internal/instancestore": 4,
	// internal/bleveindex implements storage/index on bleve (F7-07, 05-storage
	// §5S.3.8) and builds the rebuild's tasks.Definition, so it is a bridge: the
	// layer of its highest import, tasks (R4).
	"internal/bleveindex": 4,
	// internal/libfs is the byte-level filesystem layer under library
	// (F7-06, 05-library 5L.3: the no-replace rename, FICLONE/clonefile,
	// the chunked copy), so it takes library's layer (R4).
	"internal/libfs": 4,
	// internal/outboxstore is the c/outbox/ key layout, records and guard
	// protocol under outbox's relay and operator operations (F6-01, 05-events
	// §5E.5.2), so it takes outbox's layer (R4).
	"internal/outboxstore": 4,
	// L5
	"acquire": 5,
	// L6 (internal/<capability>http is coreFamily's pattern)
	"httpapi": 6, "realtime": 6, "arrfacade": 6, "ui": 6, "internal/modulehttp": 6,
	// internal/openapi implements httpapi's OpenAPI (05-http-api §3, §7): R4
	// gives an internal package the layer of the public package it implements.
	"internal/openapi": 6,
	// internal/sse is the engine under realtime's stream (F6-02, 05-realtime
	// §3): the ring, the subscriber queues and the frame writer, so it takes
	// realtime's layer (R4).
	"internal/sse": 6,
	// internal/httpmw implements httpapi's perimeter stages (05-http-api §3,
	// §4.2; R1-17), the same rule.
	"internal/httpmw": 6,
	// L7
	"module": 7,
	// L8
	"kernel": 8,
	// L9 (examples/* is coreFamily's pattern)
	"app": 9,
	// This guard's own package: test files only, so no non-test imports.
	"internal": 0,
}

// capabilityHTTP is R4's internal/<capability>http pattern (L6).
func capabilityHTTP() *regexp.Regexp { return regexp.MustCompile(`^internal/[a-z0-9]+http$`) }

// coreFamily is R4's sub-package rule and its two patterns.
func coreFamily(pkg string) (int, bool) {
	if pkg == "examples" || strings.HasPrefix(pkg, "examples/") {
		return 9, true
	}
	if capabilityHTTP().MatchString(pkg) {
		return 6, true
	}
	// A public sub-package takes its nearest named ancestor's layer. internal/*
	// is not public: an unnamed internal package is not placed here, so the
	// engine asks for a table entry (R4: the layer of the public package it
	// implements, or of its highest import for a bridge).
	if strings.HasPrefix(pkg, "internal/") {
		return 0, false
	}
	for p := pkg; strings.Contains(p, "/"); {
		p = p[:strings.LastIndex(p, "/")]
		if l, ok := coreLayerOf[p]; ok {
			return l, true
		}
	}
	return 0, false
}

// declaredLayers is coreLayerOf extended with every loaded package that the
// sub-package rule or a pattern places, so the engine's completeness check
// asks a new package for an entry only when R4 cannot place it.
func declaredLayers(t *testing.T, root string) map[string]int {
	t.Helper()
	out := map[string]int{}
	maps.Copy(out, coreLayerOf)
	pkgs, err := archtest.Load(archtest.LoadOptions{Dir: root, TagSets: archtest.TagSets()})
	if err != nil {
		t.Fatal(err)
	}
	for ip := range pkgs {
		pkg, ok := archtest.Rel(corePath, ip)
		if !ok {
			continue
		}
		if _, named := out[pkg]; named {
			continue
		}
		if l, ok := coreFamily(pkg); ok {
			out[pkg] = l
		}
	}
	return out
}

// countPackageDirs counts the directories under root that hold a .go file,
// skipping testdata, dot and underscore directories, the module's ignored npm
// trees and nested modules: the packages core holds, measured without go list.
func countPackageDirs(t *testing.T, root string) int {
	t.Helper()
	ignored := map[string]bool{"web": true, "examples/hello/web": true}
	dirs := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "node_modules" || ignored[rel]) {
				return filepath.SkipDir
			}
			if rel != "." {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			dirs[filepath.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return len(dirs)
}

// TestCoreLayering is 03 R4's guard. Its in-module control (03 §3.5.1 item 2,
// third step; MOD3-8) is app -> kernel: the composition root's import of the
// boot machinery, which exists for as long as core boots through app.New
// (R1-13). The earlier controls were health -> log (R1-09, gone when R1-36
// deleted log.Sanitize) and metrics -> log (the span exporter's
// log.Redactor, R1-32), which still exists but is no longer the control.
func TestCoreLayering(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	held := countPackageDirs(t, root)
	if held < 4 {
		t.Fatalf("the directory walk is broken: found %d package directories, want at least buildid, storage/keys, archtest and internal", held)
	}
	archtest.Layering(t, archtest.LayerSpec{
		ModulePath:          corePath,
		Dir:                 root,
		LayerOf:             declaredLayers(t, root),
		DeclaredMayBeAbsent: true,
		Named:               coreLayerOf,
		Family:              coreFamily,
		Control:             archtest.Edge{"app", "kernel"},
		ImportFloor:         0,
		TestSupport:         archtest.CoreTestSupport,
		FloorRatio:          1,
		FloorBase:           held,
		TagSets:             archtest.TagSets(),
	})
}
