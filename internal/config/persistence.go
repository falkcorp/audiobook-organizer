// file: internal/config/persistence.go
// version: 1.62.3
// guid: 9c8d7e6f-5a4b-3c2d-1e0f-9a8b7c6d5e4f
// last-edited: 2026-11-09

package config

import "sync/atomic"

// operatorRootDir is the library root this process's operator layers name,
// "" for none.
var operatorRootDir atomic.Pointer[string]

// SetOperatorRootDir records the library root the operator layers name (cmd's
// initConfig, from the operator projection): --dir, or core.library.roots set
// by ABO_CORE_LIBRARY_ROOTS or the operator file. ROOT_DIR and the file's
// root_dir are retired names that refuse the boot, so they never reach it.
func SetOperatorRootDir(dir string) { operatorRootDir.Store(&dir) }

// RootDirOverride is the library root this process's operator layers name,
// when set and not empty. books' reset hook, which resolves the library root
// from the stored settings with the service stopped, applies it over them, as
// a boot's operator lock would.
func RootDirOverride() (string, bool) {
	p := operatorRootDir.Load()
	if p == nil || *p == "" {
		return "", false
	}
	return *p, true
}
