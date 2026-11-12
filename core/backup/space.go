// file: core/backup/space.go
// version: 1.0.1
// guid: c4a7e19d-6b25-4f83-a0d1-3e9b7c52f860
// last-edited: 2026-11-12

package backup

import (
	"fmt"
	"io"
	"math"
	"math/bits"
	"strings"
)

// spaceClaim is a number of bytes a pass is about to write under dir.
type spaceClaim struct {
	dir   string
	bytes uint64
}

// requireSpace refuses, with ErrInsufficientSpace (507 at the API), a pass whose
// claims do not fit the free space of the filesystems they write to, each with
// margin bytes of headroom beyond what the pass needs. Claims that land on one
// device add up and are checked against that device once, so two directories on
// one disk are not each promised the whole of it.
//
// It is the one place every extraction asks "will this fit", before the first
// byte is written: an archive's declared sizes (the manifest's entries, a legacy
// scan's total) are not what it costs to ship, since a compressed or sparse
// entry declares far more than it occupies, and the reader's limits bound only
// what a pass may be asked to decode. A device or free-space query that fails
// fails the check: space that cannot be measured is not assumed.
//
// The errors name no path: a path is the operator's, and these reach an API
// caller as the fixed insufficient_space body.
func requireSpace(d Disk, margin uint64, claims ...spaceClaim) error {
	type need struct {
		dir   string
		bytes uint64
	}
	byDevice := make(map[uint64]*need, len(claims))
	var order []uint64
	for _, c := range claims {
		dev, err := d.device(c.dir)
		if err != nil {
			return fmt.Errorf("backup: the device of an extraction directory: %w", err)
		}
		n, ok := byDevice[dev]
		if !ok {
			n = &need{dir: c.dir}
			byDevice[dev] = n
			order = append(order, dev)
		}
		sum, carry := bits.Add64(n.bytes, c.bytes, 0)
		if carry != 0 {
			return fmt.Errorf("%w: the declared sizes overflow", ErrInsufficientSpace)
		}
		n.bytes = sum
	}
	for _, dev := range order {
		n := byDevice[dev]
		if err := requireRoom(d, margin, n.dir, n.bytes); err != nil {
			return err
		}
	}
	return nil
}

// requireRoom is the check requireSpace makes of one device: dir's filesystem
// must have bytes and margin free. It does not ask which device that is, so it
// is the check for a write with nothing to be summed with (an upload's next
// interval) on a platform where the device cannot be named; the errors are
// requireSpace's, naming no path.
func requireRoom(d Disk, margin uint64, dir string, bytes uint64) error {
	avail, err := d.avail(dir)
	if err != nil {
		return fmt.Errorf("backup: the free space of a directory: %w", err)
	}
	total, carry := bits.Add64(bytes, margin, 0)
	if carry != 0 || avail < total {
		return fmt.Errorf("%w: %d bytes free, %d needed", ErrInsufficientSpace, avail, total)
	}
	return nil
}

// uploadCheckBytes is how many bytes an upload writes between two looks at the
// free space of the backup directory: each look asks for room for the next
// interval beyond the margin, so the volume cannot be driven under the margin
// by more than the writes of one interval, whatever the upload's length (which
// nobody knows until it ends).
const uploadCheckBytes = 32 << 20

// spaceWriter writes to w and, before it writes more than the room it has
// already been promised, asks check for more: room for the next window bytes (or
// for this one write, if that is larger). A check that fails fails the write,
// with that error, before the bytes go anywhere. window bytes are promised to
// it when it is made: the caller's own check, made before the file was opened,
// is the first one.
type spaceWriter struct {
	w      io.Writer
	check  func(bytes uint64) error
	window uint64
	left   uint64 // bytes of the last promise not yet written
}

func newSpaceWriter(w io.Writer, window uint64, check func(bytes uint64) error) *spaceWriter {
	return &spaceWriter{w: w, check: check, window: window, left: window}
}

func (s *spaceWriter) Write(p []byte) (int, error) {
	if uint64(len(p)) > s.left {
		need := max(uint64(len(p)), s.window)
		if err := s.check(need); err != nil {
			return 0, err
		}
		s.left = need
	}
	n, err := s.w.Write(p)
	s.left -= min(uint64(n), s.left)
	return n, err
}

// requireSpace is the package-level check with the service's disk and the
// settings' margin, read now so a hot-reloaded margin applies at once.
func (s *Svc) requireSpace(claims ...spaceClaim) error {
	return requireSpace(s.o.Disk, s.o.settings().margin(), claims...)
}

// margin is the headroom a space check keeps beyond what it needs: SpaceMargin,
// or 2 GiB (HEAD's backupSpaceMargin) when that is 0.
func (s Settings) margin() uint64 {
	if s.SpaceMargin == 0 {
		return uint64(2 * gib)
	}
	return s.SpaceMargin
}

// declaredBytes is the sum of the sizes the manifest declares for the entries
// whose path starts with prefix ("" = every entry). A sum that does not fit is
// ErrManifest: no real archive declares 2^64 bytes.
func declaredBytes(entries []Entry, prefix string) (uint64, error) {
	var total uint64
	for _, e := range entries {
		if prefix != "" && !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		if e.Size < 0 {
			return 0, fmt.Errorf("%w: entry %q has a negative size", ErrManifest, e.Path)
		}
		sum, carry := bits.Add64(total, uint64(e.Size), 0)
		if carry != 0 {
			return 0, fmt.Errorf("%w: the entries' sizes overflow", ErrManifest)
		}
		total = sum
	}
	return total, nil
}

// addBytes is a+b, or false when it does not fit.
func addBytes(a, b uint64) (uint64, bool) {
	sum, carry := bits.Add64(a, b, 0)
	return sum, carry == 0
}

// budgetOf is a byte count as an Extractor budget (an int64: a count above the
// largest file size any filesystem holds is capped, and the space check has
// refused it long before).
func budgetOf(n uint64) int64 { return int64(min(n, math.MaxInt64)) }
