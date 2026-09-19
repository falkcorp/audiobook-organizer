// file: internal/applygate/manual_only.go
// version: 1.0.0
// guid: a2f62ab5-314e-427a-8ca7-de28de936b75
// last-edited: 2026-09-19

package applygate

import "regexp"

// manualOnlyRe matches the libraries the owner curates by hand: Doctor Who,
// Big Finish and Torchwood. Owner rule (standing): these are never touched by
// a bulk apply or bulk merge -- the owner applies them manually, by explicit
// book id. Separators between words vary across rips ("Doctor.Who",
// "Doctor_Who", "DoctorWho"), so any run of separators, or none, is accepted.
var manualOnlyRe = regexp.MustCompile(`(?i)\b(doctor[\s._-]*who|big[\s._-]*finish|torchwood)\b`)

// IsOwnerManualOnly reports whether a book with this path or series name
// belongs to a manual-only library and must be left out of every bulk apply or
// bulk merge.
func IsOwnerManualOnly(path, seriesName string) bool {
	return manualOnlyRe.MatchString(path) || manualOnlyRe.MatchString(seriesName)
}
