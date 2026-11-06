// file: core/buildid/buildid.go
// version: 1.0.0
// guid: a730e31a-05c6-4dd0-ab4e-bbec7dbaeaee
// last-edited: 2026-11-05

// Package buildid computes the binary ID of the running executable (03 §3.6
// "Build identity"): the release version, a "+", and the lowercase hex SHA-256
// of the executable file. It needs no ldflags and no .git, so a store-format
// guard and --version-json can name the exact binary before the kernel exists
// (06 §6.4.5; once it does, app.BinaryID delegates here).
//
// The binary ID is never the UI build ID (which names one built UI bundle and
// is "dev" for every API-only build) and never the core version constant.
//
// Layer L0 (03 R4): standard library only, no package-level state. Callers
// compute the ID once per process and keep it; this package does not cache.
package buildid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// Binary returns version + "+" + the lowercase hex SHA-256 of the running
// executable's file, e.g. "v1.4.0+3f9c...". It fails when the executable
// cannot be located or read; it never returns a partial ID.
func Binary(version string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("buildid: locate executable: %w", err)
	}
	sum, err := fileSHA256(exe)
	if err != nil {
		return "", err
	}
	return version + "+" + sum, nil
}

// Commit returns the VCS revision the binary was built from ("vcs.revision"
// in the build info), with "+dirty" appended when "vcs.modified" is true. It
// returns "" when the build carries no VCS stamp (go test binaries, builds
// with -buildvcs=false or outside a checkout).
func Commit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return commitFrom(info.Settings)
}

// commitFrom is Commit over an explicit settings list, so the dirty and
// missing cases are testable without a stamped binary.
func commitFrom(settings []debug.BuildSetting) string {
	var rev string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return ""
	}
	if dirty {
		return rev + "+dirty"
	}
	return rev
}

// fileSHA256 streams path through SHA-256 and returns the lowercase hex sum.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("buildid: open executable: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("buildid: hash executable: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
