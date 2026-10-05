// file: internal/franchise/franchise_test.go
// version: 1.0.1
// guid: 0e7a3c51-2d94-4b8f-a6e1-7c5d9f3b2a18
// last-edited: 2026-10-04

package franchise

import (
	"regexp"
	"strings"
	"testing"
)

// Every census range term, with the franchise and range it must report.
func TestRangeTerms(t *testing.T) {
	cases := []struct{ in, franchise, rng string }{
		{"The War Master - Series 12: Killing Time", DoctorWho, "war-master"},
		{"/lib/Big Finish/The War-Master/01.mp3", DoctorWho, "war-master"},
		{"/newbooks/wmaster11-the-war-master.mp3a/01.mp3", DoctorWho, "war-master"},
		{"Call Me Master", DoctorWho, "master"},
		{"/lib/Master!/Master! - Part 2", DoctorWho, "master"},
		{"Dark Gallifrey - Master!", DoctorWho, "gallifrey"},
		{"BBV - Auton 2", DoctorWho, "bbv"},
		{"/abooks/Doctor Who iPod Audiobooks/Audio Visuals/AV1 Space Wail", DoctorWho, "audio-visuals"},
		{"AudioVisuals Season 2", DoctorWho, "audio-visuals"},
		{"The Sirens of Time", DoctorWho, "sirens-of-time"},
		{"Dark Shadows - The Night Whispers", BigFinish, "dark-shadows"},
		{"Stargate SG-1 - Series 2", BigFinish, "stargate"},
		{"Big Finish Stargate Atlantis", BigFinish, "stargate"},
		{"The Companion Chronicles - The Three Companions", DoctorWho, "companion-chronicles"},
		{"Doctor Who - The Lost Stories", DoctorWho, "lost-stories"},
		{"/lib/The Lost Stories/Farewell, Great Macedon", DoctorWho, "lost-stories"},
		{"Short Trips - Series 13", DoctorWho, "short-trips"},
		{"/lib/Doctor.Who - Short Trips/x.mp3", DoctorWho, "short-trips"},
		{"The Eighth of March", DoctorWho, "eighth-of-march"},
		{"Susan's War", DoctorWho, "susans-war"},
		{"Susan’s War", DoctorWho, "susans-war"},
		{"Kaldor City - Occam's Razor", DoctorWho, "kaldor-city"},
		{"Zygons - Series 1", DoctorWho, "zygons"},
		{"Genesis of the Cybermen", DoctorWho, "cybermen"},
		{"I, Davros", DoctorWho, "davros"},
		{"The Sontaran Experiment", DoctorWho, "sontarans"},
		{"The Sarah Jane Adventures", DoctorWho, "sarah-jane"},
		{"Survivors Series 3", BigFinish, "survivors"},
		{"Survivors - Series 3", BigFinish, "survivors"},
		{"/lib/Timeslip/The Age of the Fourth Doctor", DoctorWho, "4th-doctor"},
		{"Timeslip - The Age of Ice", BigFinish, "timeslip"},
		{"The Time Tunnel - Series 1", BigFinish, "time-tunnel"},
		{"Star Cops - Conflict - Suspicion and Sabotage", BigFinish, "star-cops"},
		{"/newbooks/stcops02-conflict.mp3a/01.mp3", BigFinish, "star-cops"},
		{"TARDIS Tales", DoctorWho, ""},
		{"DWAD 12", DoctorWho, "dwad"},
		{"/newbooks/dwdc11d03-geronimo.mp3a/x.mp3", DoctorWho, "doctor-chronicles"},
		// The original pattern's ranges.
		{"The Thirteenth Doctor Adventures", DoctorWho, "13th-doctor"},
		{"13th Doctor", DoctorWho, "13th-doctor"},
		{"The War Doctor", DoctorWho, "war-doctor"},
		{"UNIT: Dominion", DoctorWho, "unit"},
		{"Counter-Measures Series 2", DoctorWho, "counter-measures"},
		{"Blake's 7", BigFinish, "blakes-7"},
		{"Torchwood_ Border Princes", Torchwood, ""},
		{"Doctor Who_ Mindwarp", DoctorWho, ""},
	}
	for _, c := range cases {
		h, ok := Match(c.in)
		if !ok {
			t.Errorf("Match(%q): no hit, want %s/%s", c.in, c.franchise, c.rng)
			continue
		}
		r := Detect(Evidence{Path: c.in})
		f, rng := r.Classify()
		if f != c.franchise || rng != c.rng {
			t.Errorf("Match(%q) = %s/%s (term %s), want %s/%s", c.in, f, rng, h.Term, c.franchise, c.rng)
		}
	}
}

// The false-positive list: real publishers, authors and titles that share a
// word with a range. None may match at all.
func TestFalsePositives(t *testing.T) {
	for _, s := range []string{
		"Penguin Random House", "Penguin Audio", "BBC Audio", "BBC Radio 4 Full-Cast Dramatisation",
		"BBC Audio - The Hitchhiker's Guide to the Galaxy", "Tantor Media", "Tantor Audio", "Podium Audio",
		"Podium Publishing", "Douglas Adams", "Neil Gaiman", "Adrian Tchaikovsky",
		"War Master's Gate", "War Master’s Gate", "/lib/Adrian Tchaikovsky/Shadows of the Apt/09 War Master's Gate/01.mp3",
		"Warmaster", "Melissa McShane - Warmaster",
		"Castle of Dark Shadows", "/lib/Romance/Castle of Dark Shadows/01.mp3",
		"Stargate SG-1: Hostile Ground", "Stargate Atlantis: Legacy", "Stargate: The Novel", "Fandemonium Stargate",
		"Short trips from London", "Ten Short Trips", "Lost Stories of the West", "The Lost Stories of W.S. Gilbert",
		"Survivors", "The Survivors", "Time Tunnel to Atlantis", "Puppet Master!", "Master Class",
		"Masters of Doom", "Audiovisual Translation", "Sirens", "Star Trek", "Cops and Robbers",
		"Doctor Sleep", "The Doctor's Wife", "Ugly Unit", "Countermeasures",
	} {
		if h, ok := Match(s); ok {
			t.Errorf("Match(%q) = %+v, want no hit", s, h)
		}
	}
}

// The guard keeps holding a "Missy" credit (the original pattern always did:
// widening only), but Detect reports it weak, so it never makes an auto tag
// row. Missy Elliott is on the owner's false-positive list.
func TestMissyCreditIsWeak(t *testing.T) {
	for _, who := range []string{"Missy Elliott", "Missy Cambridge"} {
		r := Detect(Evidence{Authors: []string{who}})
		if !r.Held() {
			t.Errorf("%q: not held; the guard must keep holding what it held", who)
		}
		if r.Strong() {
			t.Errorf("%q: strong signal %+v; want weak only", who, r.Signals)
		}
		r = Detect(Evidence{Narrators: []string{who}})
		if r.Strong() {
			t.Errorf("narrator %q: strong signal", who)
		}
	}
	// "Missy" in a path or series is the Big Finish range.
	if r := Detect(Evidence{Series: "Missy Series 2"}); !r.Strong() {
		t.Errorf("series Missy Series 2: want strong")
	}
}

// "Douglas Adams at the BBC" is weak-only in the census: it shares tracks
// with Doctor Who folders. Nothing about the book itself matches.
func TestDouglasAdamsAtTheBBCNotStrong(t *testing.T) {
	r := Detect(Evidence{Title: "Douglas Adams at the BBC", Authors: []string{"Douglas Adams"}, Publisher: "BBC Audio"})
	if r.Held() {
		t.Fatalf("held: %+v", r.Signals)
	}
}

// A title only the broad rule names is weak; MatchTitle's prose exclusions
// still decide strength.
func TestTitleStrength(t *testing.T) {
	for title, strong := range map[string]bool{
		"Doctor Who: Placebo Effect":      true,
		"Frontios - Doctor Who":           true,
		"Torchwood: Aliens Among Us":      true,
		"Big Finish Productions Presents": true,
		"Genesis of the Cybermen":         true,
		"The Doctor Who Fooled the World": false,
		"Big Finish to the Season":        false,
		"Secrets of the Torchwood Estate": false,
	} {
		r := Detect(Evidence{Title: title})
		if !r.Held() {
			t.Errorf("%q: not held", title)
			continue
		}
		if r.Strong() != strong {
			t.Errorf("%q: strong=%v, want %v", title, r.Strong(), strong)
		}
	}
}

func TestTagsHold(t *testing.T) {
	if _, ok := HeldByTags([]string{"favourite", "Franchise:Doctor-Who"}); !ok {
		t.Error("franchise tag not held")
	}
	if _, ok := HeldByTags([]string{"franchise:star-wars", "franchise:discworld"}); ok {
		t.Error("another franchise's tag is not an owner-manual hold")
	}
	if _, ok := HeldByTags([]string{"range:war-master", "policy:no-organize"}); ok {
		t.Error("a range tag alone is not a franchise tag")
	}
	r := Detect(Evidence{Title: "Escape from Reality", Tags: []string{"franchise:doctor-who"}})
	if !r.Held() || !r.Strong() {
		t.Error("a tagged book with a neutral title must be held")
	}
	if f, _ := r.Classify(); f != "" {
		t.Errorf("Classify counted the tag: %s", f)
	}
	if got := Tags(BigFinish, "stargate"); len(got) != 2 || got[0] != "franchise:big-finish" || got[1] != "range:stargate" {
		t.Errorf("Tags = %v", got)
	}
}

func TestClassifyBigFinishDoctorWho(t *testing.T) {
	r := Detect(Evidence{
		Path:      "/lib/Big Finish Productions/The Y Factor/The Y Factor",
		Narrators: []string{"The War Master - Series 12"},
		Authors:   []string{"Big Finish Productions"},
	})
	if f, rng := r.Classify(); f != DoctorWho || rng != "war-master" {
		t.Errorf("Classify = %s/%s, want doctor-who/war-master", f, rng)
	}
	r = Detect(Evidence{
		Path:      "/lib/Big Finish Productions/Stargate/x",
		Narrators: []string{"Stargate SG-1 - Series 2"},
		Authors:   []string{"Big Finish Productions"},
	})
	if f, rng := r.Classify(); f != BigFinish || rng != "stargate" {
		t.Errorf("Classify = %s/%s, want big-finish/stargate", f, rng)
	}
}

// ---- guard parity -------------------------------------------------------
//
// Frozen copies of the patterns the two guards used before this package
// (applygate/manual_only.go 1.9.0, repairs/guards.go 1.8.0). Everything they
// held must still be held: the change may only widen.

var oldManualOnlyRe = regexp.MustCompile(`(?i)\b(doctor[\s._-]*who|dr\.?[\s._-]*who|big[\s._-]*finish|torchwood|` +
	`(?:first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|thirteenth|fourteenth|fifteenth|` +
	`[1-9](?:st|nd|rd|th)|1[0-5]th|war|fugitive)[\s._-]*doctor|` +
	`gallifrey(?:an)?|daleks?|jago[\s._-]*(?:&|and)[\s._-]*litefoot|diary[\s._-]*of[\s._-]*river[\s._-]*song|` +
	`bernice[\s._-]*summerfield|paternoster[\s._-]*gang|missy|blake[\x{2019}']?s[\s._-]*7)\b`)
var oldUnitRe = regexp.MustCompile(`(?:^|[/\\])\s*UNIT(?:\s*[:\x{2013}\x{2014}-]|\s)`)
var oldCounterRe = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])counter[._-]+measures(?:$|[^\p{L}\p{N}])`)
var oldTitleRe = regexp.MustCompile(`(?i)` +
	`^\s*torchwood\b` + `|[-\x{2013}\x{2014}:|(\[]\s*torchwood\s*[)\]]?\s*$` + `|\bbig[\s._-]*finish[\s._-]*(?:productions|ident|audio)\b`)
var oldDWTitleRe = regexp.MustCompile(`(?i)\bdoctor[\s._-]*who\b`)
var oldDWProseRe = regexp.MustCompile(`(?i)\b(?:the|a|an)\s+(doctor\s+who)\s+[a-z]+ed\b`)

func oldMatches(s string) bool {
	f := strings.ReplaceAll(s, "_", " ")
	return oldManualOnlyRe.MatchString(f) || oldUnitRe.MatchString(f) || oldCounterRe.MatchString(s)
}

func oldTitle(title string) bool {
	f := strings.ReplaceAll(title, "_", " ")
	prose := map[int]bool{}
	for _, m := range oldDWProseRe.FindAllStringSubmatchIndex(f, -1) {
		prose[m[2]] = true
	}
	for _, m := range oldDWTitleRe.FindAllStringIndex(f, -1) {
		if !prose[m[0]] {
			return true
		}
	}
	return oldTitleRe.MatchString(f)
}

// parityCorpus is every string the old applygate and repairs tests used, plus
// census examples and near-misses.
var parityCorpus = []string{
	"/lib/Doctor Who/The Stones of Venice/01 - Part 1.mp3", "/lib/Doctor.Who - Short Trips/x.mp3",
	"/lib/BigFinish/Dalek Empire/01.mp3", "/lib/Torchwood/Outbreak/01.mp3", "Doctor Who: The Monthly Adventures",
	"/x/Unknown Author/Doctor Who_ Mindwarp/01 Part 1.mp3", "Mindwarp", "/x/Doctor_Who_Mindwarp/01.mp3",
	"/x/Torchwood_ Border Princes/01.mp3", "/x/Big_Finish_Productions/01.mp3", "Doctor Who_ Mindwarp",
	"Doctor_Who_Mindwarp", "Torchwood_ Border Princes", "Big_Finish_Productions",
	"/lib/Doctor Sleep/01 - Doctor Sleep.mp3", "/lib/The Big Sleep/01.mp3", "Philip Marlowe", "/lib/Finishing School/01.mp3",
	"/lib/Doctor Whoopsie/01.mp3", "/lib/Doctor_Whoopsie/01.mp3", "Doctor_Whoopsie", "/lib/Torchwoods_End/01.mp3",
	"The Thirteenth Doctor Adventures", "The Thirteenth Doctor Adventures - Series 1", "The First Doctor Adventures",
	"The Eighth Doctor - The Time War", "The Fifteenth Doctor", "The War Doctor", "War.Doctor", "The Fugitive Doctor",
	"13th Doctor", "The 1st Doctor Adventures", "Thirteenth_Doctor", "The Thirteenth Doctor_ Series 1", "ThirteenthDoctor",
	"The Tenth-Doctor Chronicles", "/lib/Big Finish/The Fourth Doctor Adventures/01.mp3",
	"Doctor Sleep", "Doctors Orders", "The Doctor's Wife", "Doctor Strange", "The Tenth Doctorate", "Doctor Dolittle",
	"Second Opinion", "The First Doctors", "Warm Doctor", "16th Doctor", "The Doctor Is In",
	"Dr Who", "Dr. Who and the Daleks", "Dr_Who", "DrWho", "Dr.Who - The Crusade", "Gallifrey", "Gallifrey_ Time War",
	"The Gallifreyan Chronicles", "Dalek Empire", "Daleks!", "Jago & Litefoot", "Jago and Litefoot Series 3",
	"Jago_and_Litefoot", "The Diary of River Song", "Bernice Summerfield", "Counter-Measures", "Counter_Measures",
	"Counter.Measures Series 2", "/lib/Counter-Measures/01.mp3", "The Paternoster Gang", "Missy", "Missy Series 2",
	"Blake's 7", "Blakes 7", "Blake’s 7", "Blakes_7", "UNIT: Dominion", "UNIT - Extinction", "UNIT Silenced",
	"UNIT_ Assembled", "/lib/UNIT - Nemesis/01.mp3", "/lib/UNIT Encounters/01.mp3",
	"Dr. Seuss", "Dr. Seuss - Green Eggs and Ham", "Unit Operations", "The Unit", "Community Unit Plans", "Blake's 70",
	"Countermeasure", "Countermeasures", "Counter Measures", "Jago", "River Song", "/lib/Unit Operations/01.mp3",
	"Commonwealth", "/lib/Unit - 01/01.mp3", "Unit: Shutdown", "Unit - 01",
	"Doctor Who: Placebo Effect", "Doctor Who: Apollo 23", "Doctor Who - The Daleks", "Doctor Who", "Frontios - Doctor Who",
	"Frontios (Doctor Who)", "Big Finish Productions Presents: X", "Big Finish Ident", "Torchwood: Aliens Among Us",
	"Torchwood", "The Doctor Who Fooled the World", "A Doctor Who Cared", "The Big Finish", "Big Finish to the Season",
	"Secrets of the Torchwood Estate", "Placebo Effect", "Nelvana Doctor Who", "The Language of Doctor Who",
	"Another Pirate's History of Doctor Who", "Doctor.Who - Shada", "The_Doctor_Who_Fooled_the_World",
	"Missy Elliott", "Missy Cambridge", "Big Finish Productions", "Stargate SG-1 - Series 2",
	"Christopher Eccleston_Billie Piper", "Louise Jameson_Ken Bones", "The War Master - Series 12",
}

func TestGuardParity(t *testing.T) {
	for _, s := range parityCorpus {
		if oldMatches(s) && !Matches(s) {
			t.Errorf("Matches(%q) = false; the old guard held it", s)
		}
		// The Repairs title guard: old title rule held => still held.
		if oldTitle(s) {
			if _, ok := MatchTitle(s); !ok {
				t.Errorf("MatchTitle(%q) = false; the old title rule held it", s)
			}
		}
		// Detect holds everything either old guard held, on any field.
		if oldMatches(s) || oldTitle(s) {
			for _, e := range []Evidence{{Path: s}, {Title: s}, {Series: s}, {Narrators: []string{s}}, {Authors: []string{s}}, {Publisher: s}} {
				if !Detect(e).Held() {
					t.Errorf("Detect(%+v) not held; the old guard held %q", e, s)
				}
			}
		}
	}
}

// FuzzGuardParity: whatever the old patterns held, the new ones hold.
func FuzzGuardParity(f *testing.F) {
	for _, s := range parityCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if oldMatches(s) && !Matches(s) {
			t.Errorf("Matches(%q) = false; the old guard held it", s)
		}
		if oldTitle(s) {
			if _, ok := MatchTitle(s); !ok {
				t.Errorf("MatchTitle(%q) = false; the old title rule held it", s)
			}
		}
	})
}
