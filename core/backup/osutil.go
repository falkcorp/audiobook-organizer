// file: core/backup/osutil.go
// version: 1.0.0
// guid: 3c6a9e0b-0f6c-4e0e-8a47-4d4d8e3f4a71
// last-edited: 2026-11-12

package backup

import (
	"os"
)

func osStat(p string) (os.FileInfo, error) { return os.Stat(p) }
