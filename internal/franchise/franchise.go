// file: internal/franchise/franchise.go
// version: 1.0.0
// guid: ddd241fe-c7e3-4ff5-b3df-7e2908d49f7b
// last-edited: 2026-10-04

// Package franchise is the one matcher for the libraries the owner curates by
// hand: Doctor Who, Big Finish and Torchwood. Every owner-manual guard
// (applygate's bulk-apply guard, the Repairs lane's framework guard, and the
// ~15 other callers of applygate.IsOwnerManualOnly) reads it, and the
// maintenance.tag-franchise fixer uses it to tag the books it finds, so a
// held book can be recognised again after its title or path changes.
//
// It answers three questions about a piece of text or a whole book:
//
//   - Matches / Match: does this one value name the franchise (a path, a
//     series name, a credit, a candidate field)?
//   - MatchTitle: the narrower rule for a book title, which tolerates the
//     prose shapes a title can take ("The Doctor Who Fooled the World").
//   - Detect: every signal a book carries, with the franchise and range each
//     one names, and whether it is strong or weak.
//
// The guards hold on ANY match. The matcher fails toward holding: a false
// positive costs the owner a manual apply, a miss lets a bulk apply or merge
// touch a library the owner applies by hand.
//
// Range terms beyond the original guard come from the 2026-10-04 census
// (1,730 Doctor Who / Big Finish / Torchwood books among 77,049): each is kept
// as narrow as that evidence allows. See franchise_test.go for the false
// positive list every term is checked against.
package franchise

import (
	"regexp"
	"strings"
)

// Franchise names, as they appear in a franchise tag.
const (
	DoctorWho = "doctor-who"
	BigFinish = "big-finish"
	Torchwood = "torchwood"
)

// Tag vocabulary. A tagged book carries FranchiseTagPrefix+<franchise> and,
// when a range is known, RangeTagPrefix+<range>, both with source TagSource.
const (
	TagSource          = "franchise-matcher"
	FranchiseTagPrefix = "franchise:"
	RangeTagPrefix     = "range:"
)

// Hit is one place a text names the franchise.
type Hit struct {
	// Franchise is DoctorWho, BigFinish or Torchwood.
	Franchise string `json:"franchise"`
	// Range is the range or series the term names ("war-master",
	// "companion-chronicles"), "" when the term names only the franchise
	// ("Doctor Who", "Big Finish").
	Range string `json:"range,omitempty"`
	// Term is the id of the rule that matched ("core", "war-master", ...).
	Term string `json:"term"`
	// Text is the matched text as it appears in the folded input.
	Text string `json:"text"`
}

// Fold turns every "_" into a space so a \b pattern sees a word boundary
// there. "_" is a regexp word character, so \b never fires next to it, and
// the organizer writes a colon as "_ " in folder names: "Doctor Who_
// Mindwarp" escaped every manual-only guard until this fold. Every pattern
// here matches the folded text, except counter-measures (see
// counterMeasuresRe).
func Fold(s string) string { return strings.ReplaceAll(s, "_", " ") }

// coreRe is the original owner-manual pattern (applygate.manualOnlyRe until
// 2026-10-04), verbatim, so every value it held is still held.
//
// The Doctor ranges count too, not only the literal "Doctor Who": Big Finish
// sells "The Thirteenth Doctor Adventures", "The War Doctor", "The Fugitive
// Doctor" and "The 13th Doctor" series whose names never say "Doctor Who",
// and on 2026-09-29 a junk-author trial would have minted "The Thirteenth
// Doctor Adventures" as an author for them. An ordinal ("First" ..
// "Fifteenth", or "1st" .. "15th"), "War" or "Fugitive" directly before
// "Doctor" is one. "War Doctor" also names a surgeon's memoir; the check
// fails toward holding a row. A bare "Doctor" is not ("Doctor Sleep",
// "Doctors Orders"), and neither is "The Doctor's Wife".
//
// "Dr Who" / "Dr. Who" / "DrWho" count as "Doctor Who". So do the Big Finish
// Doctor Who spin-off ranges whose names carry no Doctor word: Gallifrey
// (and "Gallifreyan"), the Daleks, Jago & Litefoot, The Diary of River Song,
// Bernice Summerfield, The Paternoster Gang, Missy, and Blake's 7 (Big
// Finish produces it). "Missy" is also a given name; holding a book by a
// Missy costs a manual apply, so the guard keeps holding it, but Detect
// reports a credit matched only by "Missy" as weak (see weakCreditTerm).
var coreRe = regexp.MustCompile(`(?i)\b(doctor[\s._-]*who|dr\.?[\s._-]*who|big[\s._-]*finish|torchwood|` +
	`(?:first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|thirteenth|fourteenth|fifteenth|` +
	`[1-9](?:st|nd|rd|th)|1[0-5]th|war|fugitive)[\s._-]*doctor|` +
	`gallifrey(?:an)?|daleks?|jago[\s._-]*(?:&|and)[\s._-]*litefoot|diary[\s._-]*of[\s._-]*river[\s._-]*song|` +
	`bernice[\s._-]*summerfield|paternoster[\s._-]*gang|missy|blake[\x{2019}']?s[\s._-]*7)\b`)

// unitRe matches Big Finish's UNIT range in its series forms only: all-caps
// "UNIT" opening a path segment or the name, then a separator or a space
// ("UNIT: Dominion", "UNIT - Extinction", "UNIT Silenced"). The word "unit"
// in any other case is not matched.
var unitRe = regexp.MustCompile(`(?:^|[/\\])\s*UNIT(?:\s*[:\x{2013}\x{2014}-]|\s)`)

// counterMeasuresRe matches Big Finish's Counter-Measures range with its
// separator ("Counter-Measures", "Counter_Measures", "Counter.Measures"),
// never "countermeasures" or "counter measures". It reads the UNFOLDED text:
// Fold would make "Counter_Measures" the phrase.
var counterMeasuresRe = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])counter[._-]+measures(?:$|[^\p{L}\p{N}])`)

// coreRange classifies a coreRe match into its franchise and range. Checked
// in order; the first match wins.
var coreRange = []struct {
	re        *regexp.Regexp
	franchise string
	rng       string
}{
	{regexp.MustCompile(`(?i)^(?:doctor|dr\.?)[\s._-]*who$`), DoctorWho, ""},
	{regexp.MustCompile(`(?i)^big[\s._-]*finish$`), BigFinish, ""},
	{regexp.MustCompile(`(?i)^torchwood$`), Torchwood, ""},
	{regexp.MustCompile(`(?i)^war[\s._-]*doctor$`), DoctorWho, "war-doctor"},
	{regexp.MustCompile(`(?i)^fugitive[\s._-]*doctor$`), DoctorWho, "fugitive-doctor"},
	{regexp.MustCompile(`(?i)doctor$`), DoctorWho, ""}, // ordinal; ordinalRange refines it
	{regexp.MustCompile(`(?i)^gallifrey`), DoctorWho, "gallifrey"},
	{regexp.MustCompile(`(?i)^daleks?$`), DoctorWho, "daleks"},
	{regexp.MustCompile(`(?i)^jago`), DoctorWho, "jago-and-litefoot"},
	{regexp.MustCompile(`(?i)^diary`), DoctorWho, "diary-of-river-song"},
	{regexp.MustCompile(`(?i)^bernice`), DoctorWho, "bernice-summerfield"},
	{regexp.MustCompile(`(?i)^paternoster`), DoctorWho, "paternoster-gang"},
	{regexp.MustCompile(`(?i)^missy$`), DoctorWho, "missy"},
	{regexp.MustCompile(`(?i)^blake`), BigFinish, "blakes-7"},
}

var ordinalWords = map[string]string{
	"first": "1st", "second": "2nd", "third": "3rd", "fourth": "4th", "fifth": "5th",
	"sixth": "6th", "seventh": "7th", "eighth": "8th", "ninth": "9th", "tenth": "10th",
	"eleventh": "11th", "twelfth": "12th", "thirteenth": "13th", "fourteenth": "14th", "fifteenth": "15th",
}

var ordinalPrefixRe = regexp.MustCompile(`(?i)^([a-z0-9]+?)[\s._-]*doctor$`)

// ordinalRange names the range of an ordinal Doctor match ("Thirteenth
// Doctor" and "13th Doctor" are both "13th-doctor").
func ordinalRange(text string) string {
	m := ordinalPrefixRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	o := strings.ToLower(m[1])
	if w, ok := ordinalWords[o]; ok {
		o = w
	}
	return o + "-doctor"
}

func classifyCore(text string) Hit {
	for _, c := range coreRange {
		if c.re.MatchString(text) {
			h := Hit{Franchise: c.franchise, Range: c.rng, Term: "core", Text: text}
			if c.franchise == DoctorWho && c.rng == "" && !strings.Contains(strings.ToLower(text), "who") {
				h.Range = ordinalRange(text)
			}
			return h
		}
	}
	return Hit{Franchise: DoctorWho, Term: "core", Text: text}
}

// extTerm is one range term added from the 2026-10-04 census. reject, when
// set, vetoes one match given the whole folded text and the match's
// position (RE2 has no lookaround, so the exclusions are checked here).
type extTerm struct {
	id        string
	franchise string
	rng       string
	re        *regexp.Regexp
	reject    func(s string, loc []int) bool
	// need, when set, must also match the same text: the term alone is too
	// common to count (Stargate novels from other publishers).
	need *regexp.Regexp
}

// segStart is "the start of the text or of a path segment, or just after a
// dash or colon separator": the place a range name sits in a folder or album
// name ("Doctor Who - Short Trips", "/Short Trips/") but not in prose
// ("short trips from London").
const segStart = `(?:^|[/\\]|[-\x{2013}\x{2014}:]\s*)`

// segEnd is the matching end: the text or segment ends, a separator follows,
// or a series/volume word or number does ("Short Trips - Series 13", "Short
// Trips 3"). "Short trips from London" and "Lost Stories of the West" stop
// at neither.
const segEnd = `(?:$|\s*[-\x{2013}\x{2014}:/\\(\[]|\s+(?:series|volume|vol\.?|box|set)\b|\s*\d)`

var extTerms = []extTerm{
	// "The War Master". Never "War Master's Gate" (Adrian Tchaikovsky). At
	// least one separator is required between the words: Melissa McShane's
	// "Warmaster" is a census false positive (census §1, precision notes) and
	// Big Finish never spells the range as one word.
	{id: "war-master", franchise: DoctorWho, rng: "war-master",
		re:     regexp.MustCompile(`(?i)\bwar[\s.-]+master\b`),
		reject: followedByPossessive},
	{id: "call-me-master", franchise: DoctorWho, rng: "master",
		re: regexp.MustCompile(`(?i)\bcall[\s.-]*me[\s.-]*master\b`)},
	// "Master!" (Big Finish), only where a range name sits: never "Puppet
	// Master!" in the middle of a title.
	{id: "master-bang", franchise: DoctorWho, rng: "master",
		re: regexp.MustCompile(`(?i)` + segStart + `master!`)},
	{id: "bbv", franchise: DoctorWho, rng: "bbv",
		re: regexp.MustCompile(`(?i)\bbbv\b`)},
	{id: "audio-visuals", franchise: DoctorWho, rng: "audio-visuals",
		re: regexp.MustCompile(`(?i)\baudio[\s.-]*visuals\b`)},
	{id: "sirens-of-time", franchise: DoctorWho, rng: "sirens-of-time",
		re: regexp.MustCompile(`(?i)\bsirens[\s.-]*of[\s.-]*time\b`)},
	// Big Finish's Dark Shadows. Never "Castle of Dark Shadows" (a romance
	// novel): a match right after "of" is rejected.
	{id: "dark-shadows", franchise: BigFinish, rng: "dark-shadows",
		re:     regexp.MustCompile(`(?i)\bdark[\s.-]*shadows\b`),
		reject: precededByOf},
	// Big Finish's Stargate, only with Big Finish context in the same text:
	// a "Series N" album name ("Stargate SG-1 - Series 2") or the studio.
	// Stargate novels from other publishers carry neither.
	{id: "stargate", franchise: BigFinish, rng: "stargate",
		re:   regexp.MustCompile(`(?i)\bstargate\b`),
		need: regexp.MustCompile(`(?i)\bseries[\s.-]*\d|\bbig[\s.-]*finish\b`)},
	{id: "companion-chronicles", franchise: DoctorWho, rng: "companion-chronicles",
		re: regexp.MustCompile(`(?i)\bcompanion[\s.-]*chronicles\b`)},
	{id: "lost-stories", franchise: DoctorWho, rng: "lost-stories",
		re: regexp.MustCompile(`(?i)` + segStart + `the[\s.-]*lost[\s.-]*stories` + segEnd)},
	{id: "short-trips", franchise: DoctorWho, rng: "short-trips",
		re: regexp.MustCompile(`(?i)` + segStart + `short[\s.-]*trips` + segEnd)},
	{id: "eighth-of-march", franchise: DoctorWho, rng: "eighth-of-march",
		re: regexp.MustCompile(`(?i)\beighth[\s.-]*of[\s.-]*march\b`)},
	{id: "susans-war", franchise: DoctorWho, rng: "susans-war",
		re: regexp.MustCompile(`(?i)\bsusan[\x{2019}']?s[\s.-]*war\b`)},
	{id: "kaldor-city", franchise: DoctorWho, rng: "kaldor-city",
		re: regexp.MustCompile(`(?i)\bkaldor[\s.-]*city\b`)},
	{id: "zygon", franchise: DoctorWho, rng: "zygons",
		re: regexp.MustCompile(`(?i)\bzygons?\b`)},
	{id: "cybermen", franchise: DoctorWho, rng: "cybermen",
		re: regexp.MustCompile(`(?i)\bcybermen\b`)},
	{id: "davros", franchise: DoctorWho, rng: "davros",
		re: regexp.MustCompile(`(?i)\bdavros\b`)},
	{id: "sontaran", franchise: DoctorWho, rng: "sontarans",
		re: regexp.MustCompile(`(?i)\bsontarans?\b`)},
	{id: "sarah-jane", franchise: DoctorWho, rng: "sarah-jane",
		re: regexp.MustCompile(`(?i)\bsarah[\s.-]*jane[\s.-]*(?:smith|adventures)\b`)},
	{id: "tardis", franchise: DoctorWho, rng: "",
		re: regexp.MustCompile(`(?i)\btardis\b`)},
	{id: "dwad", franchise: DoctorWho, rng: "dwad",
		re: regexp.MustCompile(`(?i)\bdwad\b`)},
	// Big Finish's Survivors, only with its series number ("Survivors
	// Series 3"): "Survivors" alone is a common title.
	{id: "survivors", franchise: BigFinish, rng: "survivors",
		re: regexp.MustCompile(`(?i)\bsurvivors[\s.-]*(?:-[\s.]*)?series[\s.-]*\d`)},
	{id: "timeslip", franchise: BigFinish, rng: "timeslip",
		re: regexp.MustCompile(`(?i)` + segStart + `timeslip` + segEnd)},
	{id: "time-tunnel", franchise: BigFinish, rng: "time-tunnel",
		re: regexp.MustCompile(`(?i)\bthe[\s.-]*time[\s.-]*tunnel\b`)},
	{id: "star-cops", franchise: BigFinish, rng: "star-cops",
		re: regexp.MustCompile(`(?i)\bstar[\s.-]*cops\b`)},
}

// followedByPossessive rejects a match followed by "'s" ("War Master's
// Gate").
func followedByPossessive(s string, loc []int) bool {
	rest := s[loc[1]:]
	return strings.HasPrefix(rest, "'s") || strings.HasPrefix(rest, "’s") ||
		strings.HasPrefix(rest, "'S") || strings.HasPrefix(rest, "’S")
}

var ofBeforeRe = regexp.MustCompile(`(?i)\bof[\s.-]+$`)

// precededByOf rejects a match right after "of" ("Castle of Dark Shadows").
func precededByOf(s string, loc []int) bool {
	return ofBeforeRe.MatchString(s[:loc[0]])
}

// bfDownloadRe matches a Big Finish download folder or file name: a SKU (a
// letter prefix, a number, optional letters/digits), a dash, a slug and a
// ".mp3" + letter suffix ("wmaster11-the-war-master.mp3a",
// "dwdc11d03-geronimo.mp3a"). In the census disk listing every one of the
// 101 such folders was Big Finish. Case-sensitive, as Big Finish writes
// them.
var bfDownloadRe = regexp.MustCompile(`(?:^|[/\\])([a-z]+)(\d+)[a-z0-9]*-[a-z0-9-]+\.mp3[a-z](?:[/\\]|$)`)

// bfSKURange names the range of a known Big Finish SKU prefix.
var bfSKURange = map[string]struct{ franchise, rng string }{
	"wmaster":  {DoctorWho, "war-master"},
	"stcops":   {BigFinish, "star-cops"},
	"timetun":  {BigFinish, "time-tunnel"},
	"dwdc":     {DoctorWho, "doctor-chronicles"},
	"masterer": {DoctorWho, "master"},
	"mastersd": {DoctorWho, "master"},
}

// MatchBFDownload reports a Big Finish download folder or file name in a
// path.
func MatchBFDownload(path string) (Hit, bool) {
	m := bfDownloadRe.FindStringSubmatchIndex(path)
	if m == nil {
		return Hit{}, false
	}
	prefix := path[m[2]:m[3]]
	h := Hit{Franchise: BigFinish, Term: "bf-download", Text: strings.Trim(path[m[0]:m[1]], `/\`)}
	if r, ok := bfSKURange[prefix]; ok {
		h.Franchise, h.Range = r.franchise, r.rng
	}
	return h, true
}

// MatchAll returns every hit in one value: the original pattern, UNIT,
// Counter-Measures, the census range terms and a Big Finish download name.
func MatchAll(s string) []Hit {
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

// Match returns the most specific hit in one value (one naming a range, if
// any does) and whether there is one.
func Match(s string) (Hit, bool) {
	return best(MatchAll(s))
}

// Matches reports whether one value names the franchise.
func Matches(s string) bool {
	_, ok := Match(s)
	return ok
}

func best(hits []Hit) (Hit, bool) {
	if len(hits) == 0 {
		return Hit{}, false
	}
	for _, h := range hits {
		if h.Range != "" {
			return h, true
		}
	}
	return hits[0], true
}

// titleRe is the Repairs lane's title rule (repairs.manualOnlyTitleRe until
// 2026-10-04): Torchwood must lead ("Torchwood: ...") or be a separated tag
// ("... - Torchwood"), and the studio must be named in full ("Big Finish
// Productions"). "Secrets of the Torchwood Estate" and "Big Finish to the
// Season" are prose.
var titleRe = regexp.MustCompile(`(?i)` +
	`^\s*torchwood\b` + // leading
	`|[-\x{2013}\x{2014}:|(\[]\s*torchwood\s*[)\]]?\s*$` + // trailing tag
	`|\bbig[\s._-]*finish[\s._-]*(?:productions|ident|audio)\b`) // the studio

// doctorWhoTitleRe finds "Doctor Who" anywhere in a title; a title naming it
// counts unless every mention is the prose shape doctorWhoProseRe.
var doctorWhoTitleRe = regexp.MustCompile(`(?i)\bdoctor[\s._-]*who\b`)

// doctorWhoProseRe is the one known false-positive shape: an article, then
// "doctor who" and a past-tense verb ("The Doctor Who Fooled the World", "A
// Doctor Who Cared") -- a doctor, not the franchise.
var doctorWhoProseRe = regexp.MustCompile(`(?i)\b(?:the|a|an)\s+(doctor\s+who)\s+[a-z]+ed\b`)

// namesDoctorWho reports whether a folded title mentions Doctor Who other
// than in the prose shape.
func namesDoctorWho(title string) bool {
	prose := map[int]bool{}
	for _, m := range doctorWhoProseRe.FindAllStringSubmatchIndex(title, -1) {
		prose[m[2]] = true
	}
	for _, m := range doctorWhoTitleRe.FindAllStringIndex(title, -1) {
		if !prose[m[0]] {
			return true
		}
	}
	return false
}

// MatchTitle is the narrow rule for a book title the Repairs lane's guard
// uses: "Doctor Who" anywhere except the prose shape, Torchwood leading or as
// a separated tag, the studio named in full, or one of the census range
// terms. The bulk-apply guard reads titles through the broad Match, as it
// always has; a title only the broad rule names is a WEAK signal in Detect.
func MatchTitle(title string) (Hit, bool) {
	f := Fold(title)
	if namesDoctorWho(f) {
		return Hit{Franchise: DoctorWho, Term: "title", Text: "Doctor Who"}, true
	}
	if loc := titleRe.FindStringIndex(f); loc != nil {
		text := strings.Trim(f[loc[0]:loc[1]], " -–—:|()[]")
		fr := BigFinish
		if strings.Contains(strings.ToLower(text), "torchwood") {
			fr = Torchwood
		}
		return Hit{Franchise: fr, Term: "title", Text: text}, true
	}
	var ext []Hit
	for _, h := range MatchAll(title) {
		if h.Term != "core" && h.Term != "unit" && h.Term != "counter-measures" {
			ext = append(ext, h)
		}
	}
	return best(ext)
}

// HeldByTags reports whether a book's tags mark it: any franchise: tag,
// whatever its source (a person may tag a book by hand).
func HeldByTags(tags []string) (string, bool) {
	for _, t := range tags {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), FranchiseTagPrefix) {
			return t, true
		}
	}
	return "", false
}

// Tags returns the tags a franchise and range are recorded as.
func Tags(franchise, rng string) []string {
	if franchise == "" {
		return nil
	}
	out := []string{FranchiseTagPrefix + franchise}
	if rng != "" {
		out = append(out, RangeTagPrefix+rng)
	}
	return out
}
