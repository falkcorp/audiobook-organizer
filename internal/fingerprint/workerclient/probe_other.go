// file: internal/fingerprint/workerclient/probe_other.go
// version: 1.0.0
// guid: 4c4ae371-a21a-49e8-ac82-44cf54547892
// last-edited: 2026-09-19

//go:build !unix

package workerclient

import (
	"errors"
	"runtime"
)

func writeProbe(string) error {
	return errors.New("fp-worker: the write probe is not supported on " + runtime.GOOS)
}
