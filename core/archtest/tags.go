// file: core/archtest/tags.go
// version: 1.0.1
// guid: d84b6086-3c43-4dad-a2b5-49dbff0d7005
// last-edited: 2026-11-09

package archtest

// TagSet is one (GOOS, CGO_ENABLED, build tags) combination that CI or
// goreleaser builds (03 §3.5.1 item 4).
type TagSet struct {
	// Name labels the set in failure messages.
	Name string
	// GOOS is the target OS; "" means the host's.
	GOOS string
	// CGOEnabled is "0" or "1"; "" inherits the environment.
	CGOEnabled string
	// Tags are the -tags values; nil is the default set.
	Tags []string
	// Patterns, when non-nil, restricts the set to these package patterns and
	// to a module that holds them: a set whose pattern directory does not exist
	// under the loaded module is skipped. The GOOS=windows set is the root's
	// ./cmd/mtls-bridge only, so core never lists it.
	Patterns []string
}

// TagSets returns 03 §3.5.1 item 4's one list of (GOOS, tag set) pairs, a
// fresh slice on every call (R5: no exported package-level slice). Tools that
// need every file's imports (TestCoreImportsOnlyCore, TestCoreHoldsNoAppKeyNames,
// NoInit, later NoKernelImports and SDKOnly) load the union over all of them,
// because a default-tag go list reports a build-tagged file only under
// IgnoredGoFiles, without its imports.
//
// unitlifecycle is the tag of core/kernel/fakemigration_hook.go, the test-only
// fake migration that 09 §9.2.1's unit-lifecycle job builds into its binary.
func TagSets() []TagSet {
	return []TagSet{
		{Name: "default"},
		{Name: "bench", Tags: []string{"bench"}},
		{Name: "integration", Tags: []string{"integration"}},
		{Name: "pprof", Tags: []string{"pprof"}},
		{Name: "race", Tags: []string{"race"}},
		{Name: "native_taglib", Tags: []string{"native_taglib"}},
		{Name: "unitlifecycle", Tags: []string{"unitlifecycle"}},
		{Name: "image", Tags: []string{"embed_frontend", "fts5", "native_taglib"}},
		{Name: "darwin", GOOS: "darwin", CGOEnabled: "0", Tags: []string{"embed_frontend"}},
		{Name: "windows-mtls-bridge", GOOS: "windows", Patterns: []string{"./cmd/mtls-bridge"}},
	}
}

// DefaultTagSet returns only the default set: the graph a plain go list sees.
func DefaultTagSet() []TagSet {
	return []TagSet{{Name: "default"}}
}
