// file: core/storage/import_errors.go
// version: 1.0.0
// guid: 79312b45-aa12-4865-a02d-f6ec53748b51
// last-edited: 2026-11-10

package storage

import "errors"

// ErrImportConflict: an importer found a record with the same key and
// other content. The chunk fails; the boot fails with the checkpoint open
// (05-storage §5S.3.13 "The importer contract", the create-only rule).
//
// F3-03 declares it here, ahead of F3-04's quarantine file, because
// secrets.Importer is the first importer that returns it; F3-04 keeps it
// where it is and adds the other three of §5S.3.13's sentinels.
var ErrImportConflict = errors.New("storage: an imported record already exists with other content")
