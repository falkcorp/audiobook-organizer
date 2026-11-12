// file: core/backup/osutil.go
// version: 1.0.0
// guid: 3c6a9e0b-0f6c-4e0e-8a47-4d4d8e3f4a71
// last-edited: 2026-11-12

package backup

import (
	"os"

	"github.com/falkcorp/mediacore/library"
)

func osStat(p string) (os.FileInfo, error) { return os.Stat(p) }

// osAvail is the bytes an unprivileged process can still write to the
// filesystem holding p (library.Stats: available to the service user, not the
// root reserve), with the Windows implementation the library already has.
func osAvail(p string) (uint64, error) {
	_, free, err := library.Stats(p)
	return free, err
}
