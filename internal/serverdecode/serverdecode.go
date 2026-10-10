// file: internal/serverdecode/serverdecode.go
// version: 1.0.0
// guid: 5c0e8b1a-3d47-4f62-9a15-7be2c4d90f38
// last-edited: 2026-10-09

// Package serverdecode is the single switch for in-process audio decoding.
//
// The standing rule is that audio decoding (fingerprinting, transcription,
// WAV extraction) runs on the Mac workers only; ffprobe reads are fine on the
// server. Operations that still decode on whatever host runs the server call
// Check first and refuse unless the deployment opted in with ALLOW_SERVER_DECODE.
//
// The switch is an environment variable, not a persisted setting, so an editor
// cannot flip it from the Settings page. This package imports only the
// standard library so it sits in the lowest dependency layer.
package serverdecode

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// EnvVar is the environment variable that opts a host in to server-side decoding.
const EnvVar = "ALLOW_SERVER_DECODE"

// ErrRefused is wrapped by the error Check returns when decoding is not allowed.
var ErrRefused = errors.New("audio decoding is not allowed on this server")

// Allowed reports whether the host opted in to in-process audio decoding.
// An unset or unparsable value is false.
func Allowed() bool {
	v, err := strconv.ParseBool(os.Getenv(EnvVar))
	return err == nil && v
}

// Check returns nil when decoding is allowed, otherwise an error wrapping
// ErrRefused that names the operation.
func Check(opID string) error {
	if Allowed() {
		return nil
	}
	return fmt.Errorf("%s: %w: decoding runs on the Mac workers; set %s=1 only on a development host", opID, ErrRefused, EnvVar)
}
