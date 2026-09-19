// file: internal/applygate/manual_only_test.go
// version: 1.0.0
// guid: ed721904-7696-436b-95ae-8ef5a85c91aa
// last-edited: 2026-09-19

package applygate

import "testing"

func TestIsOwnerManualOnly(t *testing.T) {
	yes := [][2]string{
		{"/lib/Doctor Who/The Stones of Venice/01 - Part 1.mp3", ""},
		{"/lib/Doctor.Who - Short Trips/x.mp3", ""},
		{"/lib/BigFinish/Dalek Empire/01.mp3", ""},
		{"/lib/Torchwood/Outbreak/01.mp3", ""},
		{"/lib/Audio Drama/x.mp3", "Doctor Who: The Monthly Adventures"},
	}
	for _, c := range yes {
		if !IsOwnerManualOnly(c[0], c[1]) {
			t.Errorf("IsOwnerManualOnly(%q, %q) = false, want true", c[0], c[1])
		}
	}
	no := [][2]string{
		{"/lib/Doctor Sleep/01 - Doctor Sleep.mp3", ""},
		{"/lib/The Big Sleep/01.mp3", "Philip Marlowe"},
		{"/lib/Finishing School/01.mp3", ""},
	}
	for _, c := range no {
		if IsOwnerManualOnly(c[0], c[1]) {
			t.Errorf("IsOwnerManualOnly(%q, %q) = true, want false", c[0], c[1])
		}
	}
}
