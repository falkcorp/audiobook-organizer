// file: core/version/version.go
// version: 1.0.0
// guid: 5694ba4f-8a17-496c-ac5e-9a22029c98a7
// last-edited: 2026-11-07

// Package version holds core's own version.
//
// Version is bumped only by the "core version" PR, and CI keeps it at or above
// the newest checkpoint (12 §12.4.3). It is a literal on purpose: it is never
// derived from debug.ReadBuildInfo, so a build from any tree reports the same
// value for the same source.
package version

// Version is core's semantic version (12 §12.4.3, "Initial value").
const Version = "0.1.0"
