// file: core/internal/libfs/fdwalk_other_test.go
// version: 1.0.0
// guid: 5c8e2d71-0a4b-49f3-b6d2-e19a7c3f0584
// last-edited: 2026-11-11

//go:build !unix

package libfs

import (
	"errors"
	"os"
	"testing"
)

// Without directory descriptors the walk and the relative open say so: a caller
// that needs them refuses.
func TestDescriptorWalkIsUnsupportedWithoutDescriptors(t *testing.T) {
	r, _ := openRoot(t)
	d := openDir(t, r, ".")
	if _, err := Ancestry(d.File(), ID{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Ancestry = %v, want ErrUnsupported", err)
	}
	if _, err := OpenAt(d, "x", os.O_RDONLY); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("OpenAt = %v, want ErrUnsupported", err)
	}
}
