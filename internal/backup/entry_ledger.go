// file: internal/backup/entry_ledger.go
// version: 0.1.0
// guid: 75c8307a-ad35-42f8-8416-17bea91fb3cc
// last-edited: 2026-11-22

package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"sort"
)

// entryLedger records, as extractArchive writes each regular file, its size and
// SHA-256, and the compression the archive was read with. A restore staged for
// a core boot needs them in its legacy manifest (corebackup.WriteLegacyStaged),
// and a legacy archive has no per-entry digests of its own, so the digest is the
// one taken from the bytes as they pass into the file: the same bytes, in the
// same read, that were written, with no second walk over the staged store. A nil
// ledger records nothing.
type entryLedger struct {
	codec string
	files map[string]ledgerFile
}

type ledgerFile struct {
	size   int64
	sha256 string
}

func newEntryLedger() *entryLedger { return &entryLedger{files: map[string]ledgerFile{}} }

// add records the file written at name (the cleaned, slash-separated entry
// name). An archive that names a path twice leaves the later entry in place,
// since writeEntry renames over the earlier one, so the later one is recorded.
func (l *entryLedger) add(name string, size int64, sum string) {
	l.files[name] = ledgerFile{size: size, sha256: sum}
}

// names is the recorded paths, sorted.
func (l *entryLedger) names() []string {
	out := make([]string, 0, len(l.files))
	for n := range l.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ledgerReader counts and hashes what passes through it.
type ledgerReader struct {
	r io.Reader
	h hash.Hash
	n int64
}

func newLedgerReader(r io.Reader) *ledgerReader { return &ledgerReader{r: r, h: sha256.New()} }

func (l *ledgerReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.h.Write(p[:n])
	l.n += int64(n)
	return n, err
}

func (l *ledgerReader) sum() string { return hex.EncodeToString(l.h.Sum(nil)) }
