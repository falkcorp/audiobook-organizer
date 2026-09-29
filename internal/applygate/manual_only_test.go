// file: internal/applygate/manual_only_test.go
// version: 1.1.0
// guid: ed721904-7696-436b-95ae-8ef5a85c91aa
// last-edited: 2026-09-29

package applygate

import "testing"

func TestIsOwnerManualOnly(t *testing.T) {
	yes := [][2]string{
		{"/lib/Doctor Who/The Stones of Venice/01 - Part 1.mp3", ""},
		{"/lib/Doctor.Who - Short Trips/x.mp3", ""},
		{"/lib/BigFinish/Dalek Empire/01.mp3", ""},
		{"/lib/Torchwood/Outbreak/01.mp3", ""},
		{"/lib/Audio Drama/x.mp3", "Doctor Who: The Monthly Adventures"},
		// "_" is a regexp word character, so \b never fired next to it; the
		// organizer writes a colon as "_ " (#3616 review F1).
		{"/x/Unknown Author/Doctor Who_ Mindwarp/01 Part 1.mp3", "Mindwarp"},
		{"/x/Doctor_Who_Mindwarp/01.mp3", ""},
		{"/x/Torchwood_ Border Princes/01.mp3", ""},
		{"/x/Big_Finish_Productions/01.mp3", ""},
		{"/lib/Audio Drama/x.mp3", "Doctor Who_ Mindwarp"},
		{"/lib/Audio Drama/x.mp3", "Doctor_Who_Mindwarp"},
		{"/lib/Audio Drama/x.mp3", "Torchwood_ Border Princes"},
		{"/lib/Audio Drama/x.mp3", "Big_Finish_Productions"},
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
		{"/lib/Doctor Whoopsie/01.mp3", ""},
		{"/lib/Doctor_Whoopsie/01.mp3", "Doctor_Whoopsie"},
		{"/lib/Torchwoods_End/01.mp3", ""},
	}
	for _, c := range no {
		if IsOwnerManualOnly(c[0], c[1]) {
			t.Errorf("IsOwnerManualOnly(%q, %q) = true, want false", c[0], c[1])
		}
	}
}
