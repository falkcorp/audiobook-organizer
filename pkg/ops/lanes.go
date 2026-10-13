// file: pkg/ops/lanes.go
// version: 1.0.0
// guid: a69cf999-b690-4991-afb3-fb97d42f1572
// last-edited: 2026-10-13

package ops

// Lane names an executor class an op may hand items to. In-process is
// implicit and needs no lane.
type Lane string

// LaneMac is the remote workers on the Macs (the fpworker protocol). It is
// the ONLY lane allowed to decode audio (fingerprinting, transcription). A def
// that decodes must declare it and must not fall back to in-process decoding.
const LaneMac Lane = "mac"
