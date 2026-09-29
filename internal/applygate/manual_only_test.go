// file: internal/applygate/manual_only_test.go
// version: 1.3.0
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

// The Doctor ranges: Big Finish series named for a Doctor, never "Doctor
// Who" (the 2026-09-29 junk-author trial minted "The Thirteenth Doctor
// Adventures" as an author).
func TestIsOwnerManualOnly_DoctorRanges(t *testing.T) {
	for _, s := range []string{
		"The Thirteenth Doctor Adventures", "The Thirteenth Doctor Adventures - Series 1",
		"The First Doctor Adventures", "The Eighth Doctor - The Time War", "The Fifteenth Doctor",
		"The War Doctor", "War.Doctor", "The Fugitive Doctor", "13th Doctor", "The 1st Doctor Adventures",
		"Thirteenth_Doctor", "The Thirteenth Doctor_ Series 1", "ThirteenthDoctor", "The Tenth-Doctor Chronicles",
		"/lib/Big Finish/The Fourth Doctor Adventures/01.mp3",
	} {
		if !IsOwnerManualOnly("", s) || !IsOwnerManualOnly(s, "") {
			t.Errorf("IsOwnerManualOnly(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"Doctor Sleep", "Doctors Orders", "The Doctor's Wife", "Doctor Strange", "The Tenth Doctorate",
		"Doctor Dolittle", "Second Opinion", "The First Doctors", "Warm Doctor", "16th Doctor", "The Doctor Is In",
	} {
		if IsOwnerManualOnly("", s) || IsOwnerManualOnly(s, "") {
			t.Errorf("IsOwnerManualOnly(%q) = true, want false", s)
		}
	}
}

// "Dr Who" spellings and the Big Finish spin-off ranges with no Doctor word.
// Holding a non-Doctor Who book costs a manual apply; missing one breaks the
// owner rule, so the list errs toward holding ("Gallifreyan", any Dalek).
func TestIsOwnerManualOnly_DrWhoAndSpinOffs(t *testing.T) {
	for _, s := range []string{
		"Dr Who", "Dr. Who and the Daleks", "Dr_Who", "DrWho", "Dr.Who - The Crusade",
		"Gallifrey", "Gallifrey_ Time War", "The Gallifreyan Chronicles", "Dalek Empire", "Daleks!",
		"Jago & Litefoot", "Jago and Litefoot Series 3", "Jago_and_Litefoot", "The Diary of River Song",
		"Bernice Summerfield", "Counter-Measures", "Counter Measures Series 2", "The Paternoster Gang",
		"Missy", "Missy Series 2", "Blake's 7", "Blakes 7", "Blake’s 7", "Blakes_7",
		"UNIT: Dominion", "UNIT - Extinction", "UNIT Silenced", "UNIT_ Assembled", "Unit: Shutdown",
		"/lib/UNIT - Nemesis/01.mp3", "/lib/UNIT Encounters/01.mp3",
	} {
		if !IsOwnerManualOnly("", s) || !IsOwnerManualOnly(s, "") {
			t.Errorf("IsOwnerManualOnly(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"Dr. Seuss", "Dr. Seuss - Green Eggs and Ham", "Unit Operations", "The Unit", "Community Unit Plans",
		"Blake's 70", "Countermeasure", "Jago", "River Song", "/lib/Unit Operations/01.mp3", "Commonwealth",
	} {
		if IsOwnerManualOnly("", s) || IsOwnerManualOnly(s, "") {
			t.Errorf("IsOwnerManualOnly(%q) = true, want false", s)
		}
	}
}
