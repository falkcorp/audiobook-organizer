// file: core/internal/archtest_layers_test.go
// version: 1.0.6
// guid: 2d3bc73d-ccb4-47c7-92d8-0d5c18bcfb8a
// last-edited: 2026-11-08

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
// imports them (coreTestSupport).
//
// R1-09, R1-36 and R1-13 edit this file only to change the in-module control (03
// §3.5.1 item 2), never this table; the exceptions are R1-13's
// internal/openapi entry, R1-20's internal/pebble entry and R1-16's internal/sdnotify entry, R4 packages the
// table missed.
var coreLayerOf = map[string]int{
	// L0
	"entity": 0, "lifecycle": 0, "version": 0, "buildid": 0, "log": 0, "metrics": 0,
	"storage/keys": 0, "storage/codec": 0, "storage/restorejournal": 0,
	"internal/keyedmutex": 0, "internal/jsonschemax": 0,
	// internal/sdnotify (R1-16) is the systemd notify protocol over the
	// standard library; its one core import is lifecycle's constants.
	"internal/sdnotify": 0,
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
	// L5
	"acquire": 5,
	// L6 (internal/<capability>http is coreFamily's pattern)
	"httpapi": 6, "realtime": 6, "arrfacade": 6, "ui": 6, "internal/modulehttp": 6,
	// internal/openapi implements httpapi's OpenAPI (05-http-api §3, §7): R4
	// gives an internal package the layer of the public package it implements.
	"internal/openapi": 6,
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

// coreTestSupport reports R4's test-support packages.
func coreTestSupport(pkg string) bool {
	for _, root := range []string{"coretest", "archtest"} {
		if pkg == root || strings.HasPrefix(pkg, root+"/") {
			return true
		}
	}
	// <pkg>/<pkg>test, e.g. tasks/taskstest, storage/index/indextest.
	if i := strings.LastIndex(pkg, "/"); i > 0 {
		parent, last := pkg[:i], pkg[i+1:]
		return last == parent[strings.LastIndex(parent, "/")+1:]+"test"
	}
	return false
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
		TestSupport:         coreTestSupport,
		FloorRatio:          1,
		FloorBase:           held,
		TagSets:             archtest.TagSets(),
	})
}
