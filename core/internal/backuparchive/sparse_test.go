// file: core/internal/backuparchive/sparse_test.go
// version: 1.0.0
// guid: 7d2e91c4-5a03-4b68-9c1f-e84b30a6d172
// last-edited: 2026-11-12

package backuparchive_test

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// paxRecord is one PAX extended-header record, "<length> <key>=<value>\n", the
// length counting its own digits.
func paxRecord(k, v string) string {
	body := " " + k + "=" + v + "\n"
	n := len(body)
	total := n + len(strconv.Itoa(n))
	if len(strconv.Itoa(total)) != len(strconv.Itoa(n)) {
		total++
	}
	return strconv.Itoa(total) + body
}

// tarHeader is one 512-byte ustar header block, written by hand: archive/tar's
// Writer drops GNU.sparse records from a regular header and refuses to encode an
// extended header itself, so the sparse fixture cannot be made with it.
func tarHeader(name string, typ byte, size int64) []byte {
	b := make([]byte, 512)
	copy(b[0:100], name)
	copy(b[100:108], "0000644\x00")
	copy(b[108:116], "0000000\x00")
	copy(b[116:124], "0000000\x00")
	copy(b[124:136], fmt.Sprintf("%011o\x00", size))
	copy(b[136:148], "00000000000\x00")
	copy(b[148:156], "        ")
	b[156] = typ
	copy(b[257:265], "ustar\x0000")
	sum := 0
	for _, c := range b {
		sum += int(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

func padded(b []byte) []byte {
	if r := len(b) % 512; r != 0 {
		b = append(b, make([]byte, 512-r)...)
	}
	return b
}

// paxSparseTar is a tar of one PAX 0.1 sparse file: five bytes of data at offset
// zero and a logical size of realSize, which a tar reader serves as realSize
// bytes, the holes read back as zeros.
func paxSparseTar(t *testing.T, name string, realSize int64) string {
	t.Helper()
	recs := paxRecord("GNU.sparse.major", "0") + paxRecord("GNU.sparse.minor", "1") +
		paxRecord("GNU.sparse.numblocks", "1") + paxRecord("GNU.sparse.map", "0,5") +
		paxRecord("GNU.sparse.name", name) + paxRecord("GNU.sparse.realsize", strconv.FormatInt(realSize, 10))
	var raw []byte
	raw = append(raw, tarHeader("PaxHeaders.0/hole", 'x', int64(len(recs)))...)
	raw = append(raw, padded([]byte(recs))...)
	raw = append(raw, tarHeader(name, '0', 5)...)
	raw = append(raw, padded([]byte("wayne"))...)
	raw = append(raw, make([]byte, 1024)...)
	p := filepath.Join(t.TempDir(), "sparse.tar")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSparseFixtureIsWhatTheStandardReaderAdmits proves the fixture below is a
// real sparse file: archive/tar itself reads it as a regular file of the
// logical size, so the refusal that follows is not passing on a malformed tar.
func TestSparseFixtureIsWhatTheStandardReaderAdmits(t *testing.T) {
	const logical = int64(1) << 30
	f, err := os.Open(paxSparseTar(t, "db/000007.sst", logical))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr, err := tar.NewReader(f).Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Typeflag != tar.TypeReg || hdr.Size != logical || hdr.Name != "db/000007.sst" {
		t.Fatalf("header = type %q size %d name %q, want a regular file of %d bytes", string(hdr.Typeflag), hdr.Size, hdr.Name, logical)
	}
	if _, ok := hdr.PAXRecords["GNU.sparse.major"]; !ok {
		t.Fatalf("the sparse records are not on the header: %v", hdr.PAXRecords)
	}
}

func TestReaderRefusesPAXSparseFiles(t *testing.T) {
	p := paxSparseTar(t, "db/000007.sst", int64(1)<<30)
	for _, dirs := range []bool{false, true} {
		_, err := readAll(t, p, backuparchive.Limits{}, dirs)
		if !errors.Is(err, backuparchive.ErrEntryType) {
			t.Fatalf("allowDirs=%v: err = %v, want ErrEntryType", dirs, err)
		}
		if !strings.Contains(err.Error(), "sparse") {
			t.Fatalf("allowDirs=%v: the refusal does not say why: %v", dirs, err)
		}
	}
}

// A sparse entry well inside every limit is still refused: the limits alone do
// not stop it, and the refusal comes before any byte of it is read.
func TestReaderRefusesSparseEvenWhenWithinLimits(t *testing.T) {
	p := paxSparseTar(t, "db/small.sst", 4096)
	if _, err := readAll(t, p, backuparchive.Limits{MaxEntryBytes: 1 << 20, MaxTotalBytes: 1 << 20}, false); !errors.Is(err, backuparchive.ErrEntryType) {
		t.Fatalf("err = %v, want ErrEntryType", err)
	}
}

func TestScanLegacyRefusesSparse(t *testing.T) {
	// A legacy archive is one top-level directory; the sparse file sits in it.
	p := paxSparseTar(t, "audiobooks.pebble/000007.sst", int64(1)<<30)
	if _, err := backuparchive.ScanLegacy(t.Context(), p, backuparchive.Limits{}); !errors.Is(err, backuparchive.ErrEntryType) {
		t.Fatalf("ScanLegacy = %v, want ErrEntryType", err)
	}
}

// zeros is an endless source: what a decompressor or a sparse reader looks like
// to a writer that trusts the declared size.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	clear(p)
	z.n += int64(len(p))
	return len(p), nil
}

func newExtractor(t *testing.T) (*backuparchive.Extractor, string) {
	t.Helper()
	dir := t.TempDir()
	ex, err := backuparchive.NewExtractor(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ex.Close() })
	return ex, dir
}

func TestExtractorBudgetStopsAtTheBytesAdmitted(t *testing.T) {
	ex, dir := newExtractor(t)
	ex.SetBudget(10)
	if _, err := ex.WriteFile("a", strings.NewReader("12345"), nil); err != nil {
		t.Fatalf("a file within the budget: %v", err)
	}
	// Five bytes are left: six are refused, and the file is not left behind.
	if _, err := ex.WriteFile("b", strings.NewReader("123456"), nil); !errors.Is(err, backuparchive.ErrLimit) {
		t.Fatalf("a file past the budget: err = %v, want ErrLimit", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused file was left behind: %v", err)
	}
	// A refused file is not charged: exactly the five that remain still fit.
	if _, err := ex.WriteFile("c", strings.NewReader("12345"), nil); err != nil {
		t.Fatalf("a file that exactly spends the budget: %v", err)
	}
	if _, err := ex.WriteFile("d", strings.NewReader("1"), nil); !errors.Is(err, backuparchive.ErrLimit) {
		t.Fatalf("a byte past an empty budget: err = %v, want ErrLimit", err)
	}
	if _, err := ex.WriteFile("e", strings.NewReader(""), nil); err != nil {
		t.Fatalf("an empty file past an empty budget: %v", err)
	}
}

func TestExtractorBudgetBoundsAnEndlessSource(t *testing.T) {
	ex, dir := newExtractor(t)
	ex.SetBudget(1 << 20)
	src := &zeros{}
	if _, err := ex.WriteFile("bomb", src, nil); !errors.Is(err, backuparchive.ErrLimit) {
		t.Fatalf("err = %v, want ErrLimit", err)
	}
	// The read stops one byte past the budget (io.Copy reads in chunks, so the
	// source may hand over a chunk; none of it is kept).
	if _, err := os.Lstat(filepath.Join(dir, "bomb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file was left behind: %v", err)
	}
	if src.n > 1<<20+(1<<20) {
		t.Fatalf("read %d bytes from an endless source with a 1 MiB budget", src.n)
	}
}

// With a manifest's declared size the source is cut off one byte past it, and
// the refusal stays the mismatch the callers map to ErrManifest.
func TestExtractorDeclaredSizeBoundsAnEndlessSource(t *testing.T) {
	ex, dir := newExtractor(t)
	want := &backuparchive.Expect{Size: 3, SHA256: strings.Repeat("0", 64)}
	if _, err := ex.WriteFile("lies", &zeros{}, want); !errors.Is(err, backuparchive.ErrEntryMismatch) {
		t.Fatalf("err = %v, want ErrEntryMismatch", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "lies")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file was left behind: %v", err)
	}
}

func TestExtractorWithoutABudgetIsUnbounded(t *testing.T) {
	ex, _ := newExtractor(t)
	if _, err := ex.WriteFile("a", io.LimitReader(&zeros{}, 1<<16), nil); err != nil {
		t.Fatalf("an extractor with no budget set refused a write: %v", err)
	}
}

func TestExtractorNegativeBudgetIsEmpty(t *testing.T) {
	ex, _ := newExtractor(t)
	ex.SetBudget(-5)
	if _, err := ex.WriteFile("a", strings.NewReader("x"), nil); !errors.Is(err, backuparchive.ErrLimit) {
		t.Fatalf("err = %v, want ErrLimit", err)
	}
}

func TestDefaultLimitsAreBelowWhatTheFilesystemsHold(t *testing.T) {
	if backuparchive.DefaultMaxEntryBytes > backuparchive.DefaultMaxTotalBytes {
		t.Fatalf("an entry may exceed the total: %d > %d", backuparchive.DefaultMaxEntryBytes, backuparchive.DefaultMaxTotalBytes)
	}
	if backuparchive.DefaultMaxTotalBytes > int64(4)<<40 || backuparchive.DefaultMaxEntries > 1<<18 {
		t.Fatalf("the defaults grew past the documented bounds: total %d, entries %d", backuparchive.DefaultMaxTotalBytes, backuparchive.DefaultMaxEntries)
	}
}
