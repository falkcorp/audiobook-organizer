// file: internal/franchise/matchall_bench_test.go
// version: 1.0.0
// guid: 9755db70-11fe-4b9c-a143-46d07225899b
// last-edited: 2026-10-30

package franchise

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// batmanCorpus builds the 200-value corpus the benchmark and the parity test
// read: Batman comic titles and characters in the shapes a scan sees (a bare
// title, a library path, an underscore-folded folder, a credit), with a
// minority carrying a franchise term so the hit paths run too.
func batmanCorpus() []string {
	titles := []string{
		"Batman: Year One", "The Long Halloween", "Hush", "The Killing Joke",
		"Batman: The Court of Owls", "Knightfall", "No Man's Land", "A Death in the Family",
		"The Dark Knight Returns", "Batman: Arkham Asylum", "Zero Year", "The Black Mirror",
		"Batman: Ego", "Dark Victory", "Batman and Son", "Under the Red Hood",
		"The Cult", "Batman: Noel", "Venom", "Contagion", "Batman: Gothic",
		"The Man Who Laughs", "Batman: The Last Knight on Earth", "Death of the Family",
		"Batman: Haunted Knight", "Broken City", "Strange Apparitions", "Batman: Earth One",
		"Batman: Battle for the Cowl", "Batman Incorporated",
	}
	templates := []string{
		"%s",
		"/audiobooks/Bruce Wayne/%s/01 - Chapter One.mp3",
		"/library/Alfred Pennyworth/%s",
		"%s_ Part Two",
		"Selina Kyle - %s (Unabridged)",
		"/lib/Gotham Press/Harvey Dent/%s - Book 3/part 04.m4b",
	}
	hot := []string{
		"%s - Doctor Who crossover", "/lib/Big Finish/Joker/%s/01.mp3",
		"Torchwood: %s", "%s - The Sirens of Time", "Daleks Over Gotham - %s",
		"/lib/UNIT - %s/track.mp3", "Short Trips - %s", "%s - Series 2 Stargate",
		"Counter-Measures - %s", "The War Master - %s",
	}
	var out []string
	for i := 0; len(out) < 170; i++ {
		out = append(out, fmt.Sprintf(templates[i%len(templates)], titles[i%len(titles)]))
	}
	for i := 0; len(out) < 200; i++ {
		out = append(out, fmt.Sprintf(hot[i%len(hot)], titles[(i*7)%len(titles)]))
	}
	return out
}

func BenchmarkMatchAll(b *testing.B) {
	corpus := batmanCorpus()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range corpus {
			MatchAll(s)
		}
	}
}

// oracleMatchAll is MatchAll as it was before the anchor prefilter: every
// rule's regexp runs on every value. It stays in the test file (the package's
// tests already carry their own reference copies) as the oracle for the
// filtered MatchAll: a new rule added to extTerms with a wrong anchor makes
// the parity test below fail instead of silently losing matches.
func oracleMatchAll(s string) []Hit {
	if s == "" {
		return nil
	}
	f := Fold(s)
	var out []Hit
	for _, loc := range coreRe.FindAllStringIndex(f, -1) {
		out = append(out, classifyCore(f[loc[0]:loc[1]]))
	}
	if loc := unitRe.FindStringIndex(f); loc != nil {
		out = append(out, Hit{Franchise: DoctorWho, Range: "unit", Term: "unit", Text: strings.TrimSpace(strings.Trim(f[loc[0]:loc[1]], `/\`))})
	}
	if loc := counterMeasuresRe.FindStringIndex(s); loc != nil {
		out = append(out, Hit{Franchise: DoctorWho, Range: "counter-measures", Term: "counter-measures", Text: s[loc[0]:loc[1]]})
	}
	for _, t := range extTerms {
		if t.need != nil && !t.need.MatchString(f) {
			continue
		}
		for _, loc := range t.re.FindAllStringIndex(f, -1) {
			if t.reject != nil && t.reject(f, loc) {
				continue
			}
			out = append(out, Hit{Franchise: t.franchise, Range: t.rng, Term: t.id, Text: strings.TrimLeft(f[loc[0]:loc[1]], "/\\-–—: ")})
			break
		}
	}
	if h, ok := MatchBFDownload(s); ok {
		out = append(out, h)
	}
	return out
}

// anchorSamples gives, for every rule, strings its regexp matches. Every
// anchor of the rule must occur (lowercased) in at least one sample, and
// every sample must contain at least one anchor of the rule: so an anchor
// that no matching text carries, or a rule whose matches an anchor misses,
// fails here.
var anchorSamples = map[string][]string{
	"core": {
		"Doctor Who", "Dr. Who", "Big Finish", "Torchwood", "The Thirteenth Doctor Adventures",
		"The War Doctor", "The Fugitive Doctor", "Gallifrey", "Daleks", "Jago & Litefoot",
		"The Diary of River Song", "Bernice Summerfield", "The Paternoster Gang", "Missy", "Blake's 7",
	},
	"unit":                 {"/lib/UNIT - Extinction/01.mp3"},
	"counter-measures":     {"Counter-Measures", "Counter_Measures"},
	"bf-download":          {"/new/wmaster11-the-war-master.mp3a/01.mp3"},
	"war-master":           {"The War Master"},
	"call-me-master":       {"Call Me Master"},
	"master-bang":          {"Dark Gallifrey - Master!"},
	"bbv":                  {"BBV - Auton 2"},
	"audio-visuals":        {"Audio Visuals", "AudioVisuals Season 2"},
	"sirens-of-time":       {"The Sirens of Time"},
	"dark-shadows":         {"Dark Shadows - The Night Whispers"},
	"stargate":             {"Stargate SG-1 - Series 2"},
	"companion-chronicles": {"The Companion Chronicles"},
	"lost-stories":         {"Doctor Who - The Lost Stories"},
	"short-trips":          {"Short Trips - Series 13"},
	"eighth-of-march":      {"The Eighth of March"},
	"susans-war":           {"Susan's War"},
	"kaldor-city":          {"Kaldor City"},
	"zygon":                {"Zygons - Series 1"},
	"cybermen":             {"Genesis of the Cybermen"},
	"davros":               {"Davros"},
	"sontaran":             {"The Sontarans"},
	"sarah-jane":           {"Sarah Jane Smith"},
	"tardis":               {"The TARDIS"},
	"dwad":                 {"DWAD 3"},
	"survivors":            {"Survivors Series 3"},
	"timeslip":             {"Timeslip - Series 1"},
	"time-tunnel":          {"The Time Tunnel"},
	"star-cops":            {"Star Cops"},
}

func TestAnchorsCoverEveryRule(t *testing.T) {
	check := func(id string, anchors []string, matches func(string) bool) {
		t.Helper()
		samples := anchorSamples[id]
		if len(anchors) == 0 || len(samples) == 0 {
			t.Fatalf("%s: needs anchors and samples", id)
		}
		used := map[string]bool{}
		for _, sample := range samples {
			if !matches(sample) {
				t.Errorf("%s: sample %q is not matched by the rule", id, sample)
			}
			lc := strings.ToLower(Fold(sample))
			hit := false
			for _, a := range anchors {
				if a != strings.ToLower(a) {
					t.Errorf("%s: anchor %q is not lowercase", id, a)
				}
				if strings.Contains(lc, a) {
					hit, used[a] = true, true
				}
			}
			if !hit {
				t.Errorf("%s: sample %q holds none of the anchors %v", id, sample, anchors)
			}
		}
		for _, a := range anchors {
			if !used[a] {
				t.Errorf("%s: anchor %q is in no sample the rule matches", id, a)
			}
		}
	}
	check("core", coreAnchors, func(s string) bool { return coreRe.MatchString(Fold(s)) })
	check("unit", unitAnchors, func(s string) bool { return unitRe.MatchString(Fold(s)) })
	check("counter-measures", counterMeasuresAnchors, func(s string) bool { return counterMeasuresRe.MatchString(s) })
	check("bf-download", bfDownloadAnchors, func(s string) bool { return bfDownloadRe.MatchString(s) })
	for _, term := range extTerms {
		term := term
		check(term.id, term.anchors, func(s string) bool { return term.re.MatchString(Fold(s)) })
	}
	if len(anchorSamples) != len(extTerms)+4 {
		t.Errorf("anchorSamples has %d entries, want %d (one per rule)", len(anchorSamples), len(extTerms)+4)
	}
}

// TestMatchAllMatchesOracle: the prefiltered MatchAll returns exactly the
// unfiltered result on the benchmark corpus, every sample, every
// TestRangeTerms-style input, and non-ASCII spellings that a (?i) regexp
// folds onto ASCII letters.
func TestMatchAllMatchesOracle(t *testing.T) {
	inputs := batmanCorpus()
	for _, ss := range anchorSamples {
		inputs = append(inputs, ss...)
	}
	inputs = append(inputs,
		"", "Survivors", "Castle of Dark Shadows", "War Master's Gate", "Warmaster",
		"Stargate Atlantis", "unit tests", "TARDIS_Tales", "Doctor_Who_Mindwarp",
		"Susan\u2019s War", "ſtargate - Series 2", "\u212Aaldor City", "ſuſan's war",
		"Big Finish: ſhort Trips", "/lib/DOCTOR WHO/UNIT: Dominion/x.mp3",
	)
	for _, in := range inputs {
		got, want := MatchAll(in), oracleMatchAll(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MatchAll(%q) = %+v, oracle %+v", in, got, want)
		}
	}
}
