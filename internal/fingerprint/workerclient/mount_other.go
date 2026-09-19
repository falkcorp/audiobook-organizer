// file: internal/fingerprint/workerclient/mount_other.go
// version: 1.0.0
// guid: 16d57665-b091-40b0-9dc0-2009b78d574f
// last-edited: 2026-09-19

//go:build !darwin && !linux

package workerclient

import (
	"errors"
	"runtime"
)

// statMount is unsupported here: without it the read-only gate cannot run,
// so the worker refuses to start.
func statMount(string) (MountInfo, error) {
	return MountInfo{}, errors.New("fp-worker: mount inspection is not supported on " + runtime.GOOS)
}
