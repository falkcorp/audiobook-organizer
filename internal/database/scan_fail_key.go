// file: internal/database/scan_fail_key.go
// version: 1.0.0
// guid: 15ada085-52cc-469c-8c7b-ceb7e0ea395e
// last-edited: 2026-10-03

package database

import (
	"crypto/sha256"
	"encoding/hex"
)

// ScanFailKey is the key for a file's scan-fail counter (the pathHash argument
// of IncrScanFailCount, ResetScanFailCount and GetScanFailCount): the first 8
// bytes of the SHA-256 of the path, as lowercase hex.
//
// The writer (the scanner, which increments on a failed read and resets on a
// successful one) and the reader (the quarantine service, which quarantines a
// book whose counter reaches the threshold) MUST derive the same key, or
// auto-quarantine silently stops working: increments land under one key and
// the threshold check reads another. Until 2026-10-03 the scanner and
// internal/quarantine each carried their own copy of this derivation, so
// nothing but coincidence kept them in step. It lives here, in the package
// that owns the counter and that both already import, so there is one.
func ScanFailKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}
