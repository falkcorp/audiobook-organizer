// file: pkg/ops/errors.go
// version: 1.0.0
// guid: fb6a1e5f-2ac9-42e5-83e4-03f66fc77b3c
// last-edited: 2026-10-13

package ops

import "errors"

// The Writer's refusals. opswriter returns exactly these values, so
// errors.Is works across the SDK and the executor.
var (
	ErrFenced           = errors.New("ops: run is fenced (canceled, abandoned or lease lost); write refused")
	ErrPreview          = errors.New("ops: preview mode; write recorded, not performed")
	ErrChangedSincePlan = errors.New("ops: row changed since the plan; refused")
)
