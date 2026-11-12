// file: core/internal/backuparchive/reader.go
// version: 1.0.0
// guid: 35b4ef6e-c480-4b6e-af58-f7a4310e450f
// last-edited: 2026-11-12

package backuparchive

import (
	"archive/tar"
	"bufio"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

// Header is one validated entry of an archive.
type Header struct {
	Name string // slash-separated, clean, relative; a directory's has no trailing slash
	Size int64  // 0 for a directory
	Dir  bool   // only when the Reader allows directories (a legacy archive)
}

// Reader is a hardened sequential reader of one tar archive, which it opens
// through the codec its leading bytes name. Next returns only validated
// headers: regular files (and, when allowed, directories) with a clean
// relative name, a size within Limits, no repeated name, and within the entry
// and total bounds, all checked before a byte of the entry is read. Reading
// the entry's body is bounded by its header's size, so no entry can be larger
// than it said.
//
// Call Finish after the last Next to read the codec to its end (the gzip CRC
// and the zstd checksum are verified there) and the file to its end (so a hash
// tee under it sees every byte).
type Reader struct {
	src       io.Reader
	closer    io.Closer
	cr        io.Reader
	codec     string
	tr        *tar.Reader
	lim       Limits
	allowDirs bool
	seen      map[string]bool
	n         int
	total     int64
	err       error
}

// NewReader reads an archive from src (the file, possibly under a hash tee),
// sniffing its codec from the first bytes. allowDirs admits directory entries
// (HEAD's legacy-abo/0 archives hold them); mediacore-backup/1 holds none.
func NewReader(src io.Reader, lim Limits, allowDirs bool) (*Reader, error) {
	br := bufio.NewReaderSize(src, 64<<10)
	magic, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("backuparchive: read the archive's first bytes: %w", err)
	}
	c := Sniff(magic)
	cr, err := c.NewReader(br)
	if err != nil {
		return nil, err
	}
	return &Reader{
		src: br, closer: cr, cr: cr, codec: c.Name(), tr: tar.NewReader(cr), lim: lim.withDefaults(),
		allowDirs: allowDirs, seen: map[string]bool{},
	}, nil
}

// Next returns the next entry's header, or io.EOF at the archive's end. After
// any other error the Reader is dead: every later call returns it.
func (r *Reader) Next() (Header, error) {
	if r.err != nil {
		return Header{}, r.err
	}
	h, err := r.next()
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return h, err
}

func (r *Reader) next() (Header, error) {
	hdr, err := r.tr.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Header{}, io.EOF
		}
		return Header{}, fmt.Errorf("backuparchive: read the next entry: %w", err)
	}
	var h Header
	switch hdr.Typeflag {
	case tar.TypeReg:
	case tar.TypeDir:
		if !r.allowDirs {
			return Header{}, refuse(ErrEntryType, hdr.Name, "a directory entry")
		}
		h.Dir = true
	default:
		return Header{}, refuse(ErrEntryType, hdr.Name, "type %q", string(hdr.Typeflag))
	}
	name, err := CleanName(hdr.Name, h.Dir)
	if err != nil {
		return Header{}, err
	}
	h.Name = name
	if r.n++; r.n > r.lim.MaxEntries {
		return Header{}, refuse(ErrLimit, name, "more than %d entries", r.lim.MaxEntries)
	}
	if !h.Dir {
		if hdr.Size < 0 || hdr.Size > r.lim.MaxEntryBytes {
			return Header{}, refuse(ErrLimit, name, "size %d, the limit is %d", hdr.Size, r.lim.MaxEntryBytes)
		}
		if r.total > r.lim.MaxTotalBytes-hdr.Size {
			return Header{}, refuse(ErrLimit, name, "the entries total more than %d bytes", r.lim.MaxTotalBytes)
		}
		r.total += hdr.Size
		h.Size = hdr.Size
	}
	if r.seen[name] {
		return Header{}, refuse(ErrDuplicateEntry, name, "listed twice")
	}
	r.seen[name] = true
	return h, nil
}

// Read reads the current entry's body: at most its header's size, then EOF. A
// body shorter than its header said is io.ErrUnexpectedEOF.
func (r *Reader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	return r.tr.Read(p)
}

// Codec is the name of the codec the archive's leading bytes named.
func (r *Reader) Codec() string { return r.codec }

// Count is the number of entries Next has returned.
func (r *Reader) Count() int { return r.n }

// Total is the sum of the sizes of the entries Next has returned.
func (r *Reader) Total() int64 { return r.total }

// Finish reads what follows the tar stream's end: the codec's trailer (which
// verifies its checksum) and any padding, which must be zeros, then the rest
// of the file. It is bounded by the total limit.
func (r *Reader) Finish() error {
	if r.err != nil {
		return r.err
	}
	var buf [32 << 10]byte
	var extra int64
	for {
		n, err := r.cr.Read(buf[:])
		for _, b := range buf[:n] {
			if b != 0 {
				return ErrTrailingData
			}
		}
		if extra += int64(n); extra > r.lim.MaxTotalBytes {
			return fmt.Errorf("%w: padding after the archive", ErrLimit)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("backuparchive: finish the archive: %w", err)
		}
	}
	// A gzip file may hold more members than the tar needs; anything the codec
	// did not consume is read here so a tee under src hashes the whole file.
	if _, err := io.Copy(io.Discard, io.LimitReader(r.src, r.lim.MaxArchiveBytes)); err != nil {
		return fmt.Errorf("backuparchive: finish the archive: %w", err)
	}
	return nil
}

// Close releases the codec.
func (r *Reader) Close() error { return r.closer.Close() }

// CleanName validates an entry name: a slash-separated relative path with no
// empty, `.` or `..` element, no backslash or NUL, not absolute, at most
// MaxNameBytes. A directory name may carry one trailing slash, which is
// removed. It returns the name or ErrEntryName.
func CleanName(name string, dir bool) (string, error) {
	n := name
	if dir {
		n = strings.TrimSuffix(n, "/")
	}
	switch {
	case n == "" || len(n) > MaxNameBytes:
		return "", refuse(ErrEntryName, name, "empty or longer than %d bytes", MaxNameBytes)
	case strings.ContainsAny(n, "\x00\\"):
		return "", refuse(ErrEntryName, name, "holds a NUL or a backslash")
	case strings.HasPrefix(n, "/"):
		return "", refuse(ErrEntryName, name, "absolute")
	case path.Clean(n) != n || !filepath.IsLocal(filepath.FromSlash(n)):
		return "", refuse(ErrEntryName, name, "not a clean local path")
	}
	return n, nil
}
