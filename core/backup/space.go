// file: core/backup/space.go
// version: 1.0.0
// guid: c4a7e19d-6b25-4f83-a0d1-3e9b7c52f860
// last-edited: 2026-11-12

package backup

import (
	"fmt"
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
		avail, err := d.avail(n.dir)
		if err != nil {
			return fmt.Errorf("backup: the free space of an extraction directory: %w", err)
		}
		total, carry := bits.Add64(n.bytes, margin, 0)
		if carry != 0 || avail < total {
			return fmt.Errorf("%w: %d bytes free, %d needed", ErrInsufficientSpace, avail, total)
		}
	}
	return nil
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
