// file: internal/authorjunk/authorjunk.go
// version: 1.3.0
// guid: 66089e88-ec3d-459f-8aa3-dd39a204a1e1
// last-edited: 2026-09-29

// Package authorjunk answers "is this AUTHOR ROW a person, or something the
// importer filed in the author field that is not a person?" -- a series name
// ("Demon Cycle"), a character ("Harry Potter"), a genre ("Science Fiction"),
// a book title ("Before They Are Hanged"), a placeholder ("Unknown"), a
// publisher or studio ("GraphicAudio"), a narrator credit ("read by
// narrator") or other shrapnel ("Track01", "Book 1 (Unabridged)").
//
// It is the classifier behind the Repairs lane's junk-author fixer
// (internal/plugins/maintenance/junk_author_fixer.go). It holds no new
// heuristics where an existing one answers the question:
//
//   - personname.IsPlausibleAuthorName, the author-creation gate, already
//     refuses copyright shrapnel, story counts, era tags, edition markers,
//     "read by", timecodes, pure numbers, placeholders, structural labels and
//     positional artifacts. A row it refuses is junk by the system's own
//     definition; it only predates the gate.
//   - authorname.IsPlaceholderAuthor is the placeholder list dedup uses.
//   - personname.LooksLikePersonName / LooksLikeAuthorCredit are the shape
//     tests. A name that passes LooksLikeAuthorCredit as a LIST of people
//     ("Preston & Child") is never judged here: a composite belongs to the
//     author-split tooling.
//   - The collective words ("various", "anonymous") and the possessive /
//     all-caps-shout signals are maintenance.author-path-link's
//     authorPathLinkNonPersonRow signals, restated because that function is
//     unexported and deliberately op-local. Its leading-article signal is NOT
//     used as a verdict on its own: it holds "An Na", a real author.
//
// # Two strengths
//
// A STRONG verdict is decided by the name alone ("Book 1 (Unabridged)", "Demon
// Cycle", "GraphicAudio"). A WEAK verdict comes from library evidence -- the
// name is the title of another author's book, or the name of a series another
// author's books are in -- and is NOT enough on its own: the production series
// and title tables are themselves full of swapped fields (a series named
// "Brandon Sanderson", a book titled "Joe Abercrombie"), so a weak verdict is
// only acted on when the row's own books carry evidence of a different real
// author and none of them names this row. The fixer does that check; Classify
// only reports the strength.
//
// # Series-marker words, and why "saga" is conditional
//
// The series-marker list is the one PR #3610 measured for
// personname.workWords on 2026-09-28 (15,048 production author names; every
// person-shaped name carrying one of these words was a mis-filed work). It is
// duplicated here, not imported, because #3610 is not merged; once it is,
// SeriesMarker should call personname.HasSeriesMarker. "saga" is a real
// surname (Junichi Saga), so it counts only in a name that is not a two-word
// person shape ("Forest Kingdom Saga" is flagged, "Junichi Saga" is not).
package authorjunk

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// Class is the kind of non-person an author row is.
type Class string

// The classes. Their string values are the Repairs-row class names.
const (
	ClassNone        Class = ""
	ClassSeriesName  Class = "series_name"
	ClassWorkTitle   Class = "work_title"
	ClassCharacter   Class = "character"
	ClassGenre       Class = "genre"
	ClassPlaceholder Class = "placeholder"
	ClassPublisher   Class = "publisher_or_studio"
	ClassNarrator    Class = "narrator_credit"
	ClassOther       Class = "other_non_person"
)

// Classes lists every non-empty class in display order.
var Classes = []Class{ClassSeriesName, ClassWorkTitle, ClassCharacter, ClassGenre,
	ClassPlaceholder, ClassPublisher, ClassNarrator, ClassOther}

// Strength says how a verdict was reached.
type Strength int

const (
	// StrengthNone: not junk.
	StrengthNone Strength = iota
	// Weak: library evidence only; needs the row's books to confirm.
	Weak
	// Strong: the name alone decides.
	Strong
)

// Verdict is Classify's answer.
type Verdict struct {
	Class    Class
	Strength Strength
	// Rule names the rule that fired, for the row's reason text and tests.
	Rule string
}

// Junk reports whether the verdict flags the row at all.
func (v Verdict) Junk() bool { return v.Class != ClassNone }

// Evidence is what the library says about a name. The fixer builds it from
// its whole-library index; the zero value means "no library evidence".
type Evidence struct {
	// OwnTitles: every live book the row credits has a title (or series +
	// position) that IS the name -- maintenance.author-strip-merge's
	// title-as-author rule (classifyTitleAsAuthor).
	OwnTitles bool
	// TitleOfOtherAuthor counts books credited to OTHER rows whose title is
	// the name.
	TitleOfOtherAuthor int
	// SeriesOfOtherAuthor counts books credited to OTHER rows that sit in a
	// series whose name is the name.
	SeriesOfOtherAuthor int
}

// Rule names.
const (
	RulePlaceholder     = "placeholder"
	RuleCollective      = "collective_word"
	RuleNarrator        = "narrator_phrase"
	RulePublisher       = "publisher_or_studio_name"
	RuleGenre           = "genre_name"
	RuleCreationGate    = "author_creation_gate"
	RuleSeriesMarker    = "series_marker_word"
	RuleStructuralWord  = "structural_word"
	RuleLeadingArticle  = "leading_article_title_shape"
	RulePossessive      = "possessive"
	RuleShout           = "all_caps_shout"
	RuleFilenameShape   = "filename_shape"
	RuleOwnTitles       = "own_books_titled_with_name"
	RuleTitleOfOther    = "title_of_another_authors_book"
	RuleSeriesOfOther   = "series_of_another_authors_books"
	RuleCharacterSeries = "person_shaped_series_of_another_author"
	// RuleArticleThe: "The Complete", "The Rosharan System", "The Thirteenth
	// Doctor Adventures" -- person-SHAPED, so RuleLeadingArticle misses them.
	RuleArticleThe = "leading_the_title_shape"
	// RuleNumberWord: a word that is (or starts with) a number, or a
	// letter-and-number code ("Avatars Dance 1", "Beka Cooper 2-Bloodhound",
	// "B01 Her").
	RuleNumberWord = "number_word"
	// RuleSlug: a lowercase hyphen-joined slug ("the-final-strife").
	RuleSlug = "slug"
	// RuleGluedWords: several words glued into one CamelCase token
	// ("StudyinSlaughterSchooledinMagicBook3").
	RuleGluedWords = "glued_words"
	// RuleFranchise: a media franchise ("Star Wars", "Stargate SG-1").
	RuleFranchise = "franchise_name"
	// RuleSiteTag: a release-site or uploader tag ("abooks").
	RuleSiteTag = "release_site_tag"
	// RuleSeriesParenthetical: a person with a series in parentheses
	// ("Dante King (Dragon Born)", "L. E. Miranda (Rise of the Last Star)").
	RuleSeriesParenthetical = "series_parenthetical"
)

// collectiveWords: author-path-link's list (authorPathLinkCollectiveWords),
// whole words. Kept to words that mean "no single author".
var collectiveWords = map[string]bool{
	"various": true, "anonymous": true, "unknown": true, "assorted": true,
	"multiple": true, "misc": true, "miscellaneous": true, "anthology": true,
	"compilation": true, "uncredited": true,
}

// seriesMarkers: #3610's measured workWords minus the structural words (see
// structuralWords below, which are work-title markers here) plus the plural
// and set forms that occur in this library. "saga"/"sagas" are handled
// separately (see the package comment).
var seriesMarkers = map[string]bool{
	"archive": true, "archives": true,
	"chronicle": true, "chronicles": true,
	"series": true,
	"cycle":  true, "cycles": true,
	"trilogy": true, "trilogies": true,
	"duology": true, "quartet": true, "quintet": true,
	"collection": true, "collections": true,
	"omnibus":  true,
	"universe": true,
}

// structuralWords: personname's structuralWords, but matched anywhere as a
// whole word ("Jonathan Strange and Mr Norrell part 4", "Night Angel Book 1").
// personname tests them only as the FIRST word.
var structuralWords = map[string]bool{
	"book": true, "books": true, "chapter": true, "chapters": true,
	"part": true, "parts": true, "vol": true, "vols": true,
	"volume": true, "volumes": true, "disc": true, "discs": true,
	"episode": true, "episodes": true, "unabridged": true, "abridged": true,
}

// publisherNames are whole normalized names of audiobook publishers, studios
// and production houses seen in the author field. A whole-name match only.
var publisherNames = map[string]bool{
	"audible": true, "audible studios": true, "audible originals": true,
	"audible audio": true, "audible audiobook": true,
	"graphicaudio": true, "graphic audio": true, "graphic audio llc": true,
	"big finish": true, "big finish productions": true,
	"bbc": true, "bbc radio": true, "bbc audio": true, "bbc radio 4": true,
	"bbc audiobooks": true, "bbc worldwide": true,
	"full cast": true, "full cast audio": true, "a full cast": true,
	"podium audio": true, "podium publishing": true, "tantor audio": true,
	"tantor media": true, "blackstone audio": true, "blackstone publishing": true,
	"brilliance audio": true, "recorded books": true, "random house audio": true,
	"penguin audio": true, "penguin random house audio": true,
	"harpercollins": true, "harper audio": true, "harperaudio": true,
	"macmillan audio": true, "simon schuster": true, "simon schuster audio": true,
	"simon and schuster audio": true, "hachette audio": true, "dreamscape media": true,
	"soundbooth theater": true, "audioworks": true, "listening library": true,
	"naxos audiobooks": true, "audiogo": true, "audio go": true, "isis audio": true,
	"highbridge": true, "highbridge audio": true, "hbo audio": true,
	"lit riot press": true, "aethon books": true, "mountaindale press": true,
	"moonquill press": true, "royal road": true, "wraithmarked creative": true,
	"soundings": true, "chivers": true, "chivers audio books": true,
	"audio drama": true, "radio drama": true, "dramatized adaptation": true,
	"excelsior productions": true, "a star trek fan production": true,
}

// publisherWords mark a studio anywhere in the name: "GraphicAudio [E. E.
// Knight]", "Brandon Sanderson (GraphicAudio)", "BBC - Ray Bradbury". These
// words are never part of a person's name.
var publisherWords = map[string]bool{
	"graphicaudio": true, "productions": true, "studios": true,
	"audiobooks": true, "publishing": true, "llc": true, "inc": true,
}

// genreNames are whole normalized genre names. A whole-name match only: no
// person is named "Science Fiction".
var genreNames = map[string]bool{
	"science fiction": true, "sci fi": true, "scifi": true, "sf": true,
	"fantasy": true, "epic fantasy": true, "urban fantasy": true,
	"dark fantasy": true, "high fantasy": true, "progression fantasy": true,
	"horror": true, "mystery": true, "mysteries": true, "thriller": true,
	"thrillers": true, "suspense": true, "romance": true, "litrpg": true,
	"lit rpg": true, "gamelit": true, "cultivation": true, "wuxia": true,
	"xianxia": true, "young adult": true, "ya": true, "non fiction": true,
	"nonfiction": true, "fiction": true, "literature": true, "biography": true,
	"biographies": true, "memoir": true, "history": true, "classics": true,
	"classic": true, "humor": true, "humour": true, "comedy": true,
	"drama": true, "short stories": true, "poetry": true, "self help": true,
	"business": true, "children": true, "childrens": true, "kids": true,
	"space opera": true, "military science fiction": true, "military sci fi": true,
	"cyberpunk": true, "dystopian": true, "adventure": true, "western": true,
	"westerns": true, "crime": true, "paranormal": true,
	"post apocalyptic": true, "apocalyptic": true, "science": true,
	"philosophy": true, "religion": true, "spirituality": true, "true crime": true,
	"sci fi fantasy": true, "science fiction fantasy": true, "action adventure": true,
	"harem": true, "isekai": true, "light novel": true, "light novels": true,
	"historical fiction": true, "alternate history": true, "steampunk": true,
	"superhero": true, "superheroes": true, "space": true, "audiobook": true,
	"audiobooks": true, "podcast": true, "podcasts": true, "radio": true,
	"lecture": true, "lectures": true, "general": true,
	"lesbian romance": true, "lesbian fiction": true, "lesbian erotica": true,
	"gay romance": true, "lgbt": true, "lgbtq": true, "erotica": true,
	"anime": true, "manga": true,
}

// genreWrapWords are the work nouns a genre label is wrapped in: "A LitRPG
// Novel", "An Epic Fantasy Adventure". genreCore strips a leading article and
// one trailing wrap word before the whole-name genre lookup, so "The Anime"
// and "A LitRPG Novel" are the genre they name and "Anime" alone still is.
var genreWrapWords = map[string]bool{
	"novel": true, "novels": true, "story": true, "stories": true,
	"adventure": true, "tale": true, "tales": true,
}

// genreCore is n (a Normalize result) without a leading article and one
// trailing genreWrapWords word, or "" when nothing was stripped.
func genreCore(n string) string {
	f := strings.Fields(n)
	stripped := false
	if len(f) >= 2 && (f[0] == "the" || f[0] == "a" || f[0] == "an") {
		f, stripped = f[1:], true
	}
	if len(f) >= 2 && genreWrapWords[f[len(f)-1]] {
		f, stripped = f[:len(f)-1], true
	}
	if !stripped {
		return ""
	}
	return strings.Join(f, " ")
}

// siteTags are release-site and uploader tags seen in the author field.
// Whole normalized name only.
var siteTags = map[string]bool{"abooks": true}

// franchisePhrases are media franchises, matched as a whole-word phrase
// anywhere in the normalized name ("Star Wars Full Cast Audio Drama",
// "Stargate SG-1"). Only multi-word phrases or coined words no person is
// named: "Marvel" and "Halo" are left out on purpose.
var franchisePhrases = []string{
	"star wars", "star trek", "stargate", "warhammer", "dragonlance",
	"battletech", "mechwarrior", "forgotten realms", "sword art online",
	"doctor who", "torchwood",
}

// productionPhrases mark a production credit anywhere in the name: "Star
// Wars Full Cast Audio Drama". publisherNames has them as whole names only.
var productionPhrases = []string{"full cast", "audio drama", "radio drama", "dramatized adaptation"}

// hasPhrase reports whether the normalized name n holds phrase as whole words.
func hasPhrase(n, phrase string) bool {
	return n == phrase || strings.HasPrefix(n, phrase+" ") || strings.HasSuffix(n, " "+phrase) ||
		strings.Contains(n, " "+phrase+" ")
}

// orgWords make a "The ..." name a corporate or collective credit rather than
// a title: "The Arbinger Institute", "The Brothers Grimm", "The Editors of
// Time", "The Great Courses", "The Economist".
var orgWords = map[string]bool{
	"institute": true, "institution": true, "brothers": true, "sisters": true,
	"editors": true, "editor": true, "foundation": true, "society": true,
	"company": true, "corporation": true, "group": true, "team": true,
	"staff": true, "family": true, "monks": true, "fathers": true,
	"committee": true, "council": true, "association": true, "church": true,
	"school": true, "university": true, "college": true, "center": true,
	"centre": true, "trust": true, "league": true, "club": true,
	"project": true, "collective": true, "courses": true, "partners": true,
	"associates": true, "economist": true, "times": true, "onion": true,
	"guardian": true, "journal": true, "magazine": true, "review": true,
}

var (
	// narratorRe: an explicit narrator credit. "read by" / "narrated by" is
	// also refused by the creation gate (RejectReadBy); this adds the role
	// word used as the whole credit or its suffix.
	narratorRe = regexp.MustCompile(`(?i)\b(read|narrated|performed)\s+by\b|^narrat(or|ed)\b|[-,(]\s*narrator\)?\s*$`)
	// normRe / spaceRe: normalize for the whole-name lists.
	normRe  = regexp.MustCompile(`[^\p{L}\p{N} ]+`)
	spaceRe = regexp.MustCompile(`\s+`)
	// digitGlueRe: letters and digits glued in one token, the shape of a
	// filename or track ("Ender03", "chap-01", "B0FKBZ7WV8").
	digitGlueRe = regexp.MustCompile(`\p{L}{2,}\d{2,}|\d{2,}\p{L}{2,}|-\d{2,}\b`)
)

// Normalize folds a name for the whole-name lists and the library index:
// lower case, "_" and punctuation to spaces, whitespace collapsed. It matches
// maintenance's normalizeForTitleAuthorCompare for ASCII input.
func Normalize(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "_", " "))
	s = normRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// IsCompositeCredit reports whether name is a LIST of person names
// ("Preston & Child", "Douglas Preston, Lincoln Child"). Such rows are never
// judged here: splitting a composite is the author-split tooling's job, and
// every clause is a person.
//
// A clause passes when the name-only classifier does not flag it and it
// starts with a letter. personname.LooksLikeAuthorCredit is not used: it
// needs every clause person-SHAPED, so it refuses "Preston & Child" (two
// one-word surnames) and accepts "Jonathan Smidt, Portal Books".
func IsCompositeCredit(name string) bool {
	s := strings.TrimSpace(name)
	if !hasCreditSeparator(s) {
		return false
	}
	clauses := creditSplitRe.Split(s, -1)
	n := 0
	for _, c := range clauses {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		r := []rune(c)
		if !unicode.IsLetter(r[0]) || classifyName(c).Junk() {
			return false
		}
		n++
	}
	return n >= 2
}

// creditSplitRe splits a credit list into its clauses.
var creditSplitRe = regexp.MustCompile(`(?i)\s*(?:,|;|&|\+|/|\band\b|\bwith\b)\s*`)

// Classify judges one author name with its library evidence.
func Classify(name string, ev Evidence) Verdict {
	s := strings.TrimSpace(name)
	if s == "" {
		return Verdict{}
	}
	if v := classifyName(s); v.Junk() {
		return v
	}
	if IsCompositeCredit(s) {
		return Verdict{}
	}
	personShaped := personname.LooksLikePersonName(s)
	switch {
	case ev.OwnTitles && !personShaped:
		return Verdict{Class: ClassWorkTitle, Strength: Strong, Rule: RuleOwnTitles}
	case ev.OwnTitles:
		return Verdict{Class: ClassWorkTitle, Strength: Weak, Rule: RuleOwnTitles}
	case ev.SeriesOfOtherAuthor > 0 && personShaped:
		// A person-shaped name that is another author's series is almost
		// always a character-named series: Harry Potter, Jack Reacher.
		return Verdict{Class: ClassCharacter, Strength: Weak, Rule: RuleCharacterSeries}
	case ev.SeriesOfOtherAuthor > 0:
		return Verdict{Class: ClassSeriesName, Strength: Weak, Rule: RuleSeriesOfOther}
	case ev.TitleOfOtherAuthor > 0:
		return Verdict{Class: ClassWorkTitle, Strength: Weak, Rule: RuleTitleOfOther}
	}
	return Verdict{}
}

// ClassifyName judges a name with no library evidence. Only Strong verdicts
// come out of it.
func ClassifyName(name string) Verdict {
	s := strings.TrimSpace(name)
	if s == "" {
		return Verdict{}
	}
	return classifyName(s)
}

func strong(c Class, rule string) Verdict { return Verdict{Class: c, Strength: Strong, Rule: rule} }

func classifyName(s string) Verdict {
	// Leading punctuation and edition decoration hide the words the lists
	// match ("- Unknown Author", "Big Finish (Unabridged)").
	core := strings.TrimLeftFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	full := Normalize(core)
	core = personname.StripEditionSuffix(core)
	n := Normalize(core)
	if n == "" || full == "" {
		return strong(ClassOther, RuleCreationGate)
	}
	// words are the whole string's words, decoration included, so a
	// parenthetical studio ("Brandon Sanderson (GraphicAudio)") is seen.
	words := strings.Fields(full)

	// Placeholder: the system's own non-answers first.
	if authorname.IsPlaceholderAuthor(core) {
		return strong(ClassPlaceholder, RulePlaceholder)
	}
	if ok, why := personname.IsPlausibleAuthorName(core); !ok && why == personname.RejectPlaceholder {
		return strong(ClassPlaceholder, RulePlaceholder)
	}
	// A collective word counts only in a single credit: "Various Authors" is
	// a placeholder, "J. Anderson, Various" is a credit list for the split
	// tooling.
	if !hasCreditSeparator(core) {
		for _, w := range words {
			if collectiveWords[w] {
				return strong(ClassPlaceholder, RuleCollective)
			}
		}
	}

	if narratorRe.MatchString(s) {
		return strong(ClassNarrator, RuleNarrator)
	}

	if publisherNames[n] || publisherNames[full] {
		return strong(ClassPublisher, RulePublisher)
	}
	for _, w := range words {
		if publisherWords[w] {
			return strong(ClassPublisher, RulePublisher)
		}
	}
	// "Velox Books", "Portal Books": a trailing "Books" after a name-like
	// word is an imprint. A person's name does not end in "Books".
	if len(words) >= 2 && words[len(words)-1] == "books" && !hasDigit(n) && len(words) <= 3 {
		return strong(ClassPublisher, RulePublisher)
	}
	for _, ph := range productionPhrases {
		if hasPhrase(full, ph) {
			return strong(ClassPublisher, RulePublisher)
		}
	}
	if siteTags[n] || siteTags[full] {
		return strong(ClassOther, RuleSiteTag)
	}

	if genreNames[n] || genreNames[full] || genreNames[genreCore(n)] {
		return strong(ClassGenre, RuleGenre)
	}

	personShaped := personname.LooksLikePersonName(s)
	// Series markers: "Demon Cycle", "Night Angel Trilogy", "The Stormlight
	// Archive". "saga" only outside a two-word person shape.
	for i, w := range words {
		if w == "universe" {
			// "Marvel Universe" names a series; "The Restaurant at the End of
			// the Universe" is a title (judged below).
			if i == len(words)-1 && i > 0 && !leadsWithArticle(s) {
				return strong(ClassSeriesName, RuleSeriesMarker)
			}
			continue
		}
		if seriesMarkers[w] {
			return strong(ClassSeriesName, RuleSeriesMarker)
		}
		if (w == "saga" || w == "sagas") && !(personShaped && len(words) == 2) {
			return strong(ClassSeriesName, RuleSeriesMarker)
		}
	}
	for _, w := range words {
		if structuralWords[w] {
			return strong(ClassWorkTitle, RuleStructuralWord)
		}
	}

	// The creation gate: anything it refuses is junk by the system's own
	// rule. Placeholder and read-by were handled above with their own class.
	if ok, why := personname.IsPlausibleAuthorName(s); !ok {
		switch why {
		case personname.RejectReadBy:
			return strong(ClassNarrator, RuleNarrator)
		case personname.RejectEditionMarker:
			return strong(ClassWorkTitle, RuleCreationGate)
		default:
			return strong(ClassOther, RuleCreationGate)
		}
	}

	if IsCompositeCredit(s) {
		return Verdict{}
	}

	// A word that is a number or starts with one ("Avatars Dance 1", "Beka
	// Cooper 2-Bloodhound", "203 The Key To Key To Time"), or a letter-and-
	// number code ("B01 Her"): track, volume and disc labels. A generational
	// ordinal ("Thurston Howell 3rd") is a name suffix and passes; so does a
	// single-token pen name with digits ("Borgy60", "nobody103").
	if f := strings.Fields(core); len(f) >= 2 {
		for _, w := range f {
			w = strings.Trim(w, ".,;:()[]")
			if (w != "" && unicode.IsDigit([]rune(w)[0]) && !ordinalRe.MatchString(w)) || codeWordRe.MatchString(w) {
				return strong(ClassOther, RuleNumberWord)
			}
		}
	}
	if isSlug(s) {
		return strong(ClassOther, RuleSlug)
	}
	if isGluedWords(s) {
		return strong(ClassWorkTitle, RuleGluedWords)
	}
	// "Dante King (Dragon Born)" with a parenthetical that is plainly not a
	// person ("L. E. Miranda (Rise of the Last Star)"). A person-shaped
	// parenthetical is a narrator as often as a series ("Kevin Hearne (Luke
	// Daniels)"): the name alone cannot tell, so ClassifyNameInLibrary asks
	// the library's series table.
	if head, paren, ok := splitParenthetical(s); ok && personHead(head) && seriesShapedParenthetical(paren) {
		return strong(ClassSeriesName, RuleSeriesParenthetical)
	}

	// A possessive word: "Shadow's Edge", "Ender's Game". Names do not
	// inflect that way ("O'Brien", "D'Angelo" do not end in s).
	for _, w := range strings.Fields(s) {
		lw := strings.ToLower(strings.Trim(w, ".,;:\"()[]"))
		if strings.HasSuffix(lw, "'s") || strings.HasSuffix(lw, "’s") {
			return strong(ClassWorkTitle, RulePossessive)
		}
	}
	// A leading article on a name that is NOT person-shaped is a title
	// ("The Restaurant at the End of the Universe"). A person-shaped one
	// ("An Na", "The Arbinger Institute") is left to library evidence.
	if leadsWithArticle(s) && !personShaped {
		return strong(ClassWorkTitle, RuleLeadingArticle)
	}
	// "The" is never an initial and never a given name, so a person-SHAPED
	// "The ..." is still a title, a series or a fragment of one ("The
	// Complete", "The Rosharan System", "The Thirteenth Doctor Adventures"),
	// unless it names a corporate or collective credit ("The Arbinger
	// Institute", "The Brothers Grimm"; orgWords). "A" and "An" are not
	// judged this way: "A Johnston", "A Lee Martinez" (an initial without its
	// period), "A Merrydew" and "An Na" are credits of that exact shape.
	// A stage name ("The Rock") is flagged too; that costs a held row.
	if len(words) >= 2 && words[0] == "the" && !anyOrgWord(words[1:]) {
		return strong(ClassWorkTitle, RuleArticleThe)
	}
	if shoutWords(s) >= 3 {
		return strong(ClassOther, RuleShout)
	}
	for _, ph := range franchisePhrases {
		if hasPhrase(full, ph) {
			return strong(ClassSeriesName, RuleFranchise)
		}
	}
	// Filename shrapnel: an underscore, or letters and digits glued together
	// in a multi-word name. Judged on the undecorated core, so a web-serial
	// pen name with its reader in parentheses ("nobody103 (Jack Voraces)")
	// is one token and passes.
	if strings.Contains(core, "_") || digitGlueRe.MatchString(core) && !personname.LooksLikePersonName(core) && !singleToken(core) {
		return strong(ClassOther, RuleFilenameShape)
	}
	return Verdict{}
}

// ClassifyNameInLibrary is ClassifyName plus the one name shape only the
// library can decide: a person-shaped head with a person-shaped parenthetical
// ("Dante King (Dragon Born)", "D. B. King (War Wizard)"). The parenthetical
// is a series when isSeries reports its Normalize form as a series name in
// the library; otherwise it is left alone as a narrator credit ("Kevin
// Hearne (Luke Daniels)"). The verdict is Strong: the head is the person and
// CleanedName yields it. isSeries nil is ClassifyName.
func ClassifyNameInLibrary(name string, isSeries func(normalized string) bool) Verdict {
	s := strings.TrimSpace(name)
	v := ClassifyName(s)
	if v.Junk() || isSeries == nil {
		return v
	}
	head, paren, ok := splitParenthetical(s)
	if ok && personHead(head) && personname.LooksLikePersonName(paren) && isSeries(Normalize(paren)) {
		return strong(ClassSeriesName, RuleSeriesParenthetical)
	}
	return v
}

var (
	// parenRe: a name and one trailing parenthetical, space before "(" or
	// not ("Dante King(War Mage Academy)").
	parenRe = regexp.MustCompile(`^(.*[^\s(])\s*\(([^()]+)\)\s*$`)
	// ordinalRe: a generational suffix ("3rd", "4th").
	ordinalRe = regexp.MustCompile(`(?i)^\d+(?:st|nd|rd|th)$`)
	// codeWordRe: one letter and a padded number, a disc/book code ("B01",
	// "D07").
	codeWordRe = regexp.MustCompile(`^\p{L}\d{2,}$`)
	// slugPartRe: one part of a lowercase slug.
	slugPartRe = regexp.MustCompile(`^\p{Ll}+$`)
)

// splitParenthetical splits "Head (Paren)" into its two halves.
func splitParenthetical(s string) (head, paren string, ok bool) {
	m := parenRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", false
	}
	head, paren = strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	return head, paren, head != "" && paren != ""
}

// personHead: the head of a parenthetical name is one person.
func personHead(head string) bool {
	return personname.LooksLikePersonName(head) && !hasCreditSeparator(head)
}

// seriesShapedParenthetical reports whether a parenthetical is, by its shape
// alone, a series or title: two or more words, capitalized, not a person
// shape (a lowercase connective: "Rise of the Last Star", "Feast or
// Famine"), and not a credit ("with Rebecca Moesta", "translated by X").
// A one-word parenthetical is a role or a note ("Editor", "Jr."), never
// judged.
func seriesShapedParenthetical(p string) bool {
	f := strings.Fields(p)
	if len(f) < 2 || !unicode.IsUpper([]rune(f[0])[0]) || hasCreditSeparator(p) {
		return false
	}
	if strings.Contains(" "+strings.ToLower(p)+" ", " by ") {
		return false
	}
	return !personname.LooksLikePersonName(p) || personname.HasSeriesMarker(p)
}

// isSlug: a lowercase hyphen-joined slug of three or more words
// ("the-final-strife"). A two-part one ("lavf-fate", or a hyphenated pen
// name) is not judged.
func isSlug(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	parts := strings.Split(s, "-")
	if len(parts) < 3 {
		return false
	}
	for _, p := range parts {
		if !slugPartRe.MatchString(p) {
			return false
		}
	}
	return true
}

// isGluedWords: one token of fifteen or more letters with three or more
// lower-to-upper case changes, i.e. words run together
// ("StudyinSlaughterSchooledinMagicBook3"). A CamelCase pen name has one or
// two ("SerasStreams", "RavensDagger", "OverXelous").
func isGluedWords(s string) bool {
	if strings.ContainsAny(s, " \t") || len([]rune(s)) < 15 {
		return false
	}
	humps := 0
	var prev rune
	for _, r := range s {
		if unicode.IsUpper(r) && unicode.IsLower(prev) {
			humps++
		}
		prev = r
	}
	return humps >= 3
}

// anyOrgWord reports whether any of words is an orgWords word.
func anyOrgWord(words []string) bool {
	for _, w := range words {
		if orgWords[w] {
			return true
		}
	}
	return false
}

// FoldKey is the one author-name key the junk-author fixer matches with: case,
// diacritics, punctuation and spacing all folded away, so "J.N. Chaney" and
// "J. N. Chaney", "Bryce OConnor" and "Bryce O'Connor", "Emma Törzs" and
// "Emma Torzs", "Ursula K. Le Guin" and "Ursula K. LeGuin",
// "Michael-Scott Earle" and "Michael Scott Earle" are one key. The dedup
// normalizer does not fold case; the store's lookup folds only case and
// whitespace; either alone mints a near-duplicate row.
func FoldKey(name string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(name) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// protectedCollectiveWords are the collective words that name a real credit
// on an anthology or an anonymous work ("Various Authors", "Anonymous",
// "Anthology Editor"). A book credited to one is not left with no author.
var protectedCollectiveWords = map[string]bool{
	"various": true, "anonymous": true, "anon": true, "anthology": true,
	"compilation": true, "assorted": true, "multiple": true,
}

// IsCollectiveCredit reports whether name is a collective credit a book may
// legitimately carry: "Various Authors", "Anonymous (Beowulf)", "Anthology
// Editor". The junk-author fixer relinks away from one on evidence but never
// unlinks one.
func IsCollectiveCredit(name string) bool {
	s := strings.TrimSpace(name)
	if s == "" || hasCreditSeparator(s) {
		return false
	}
	for _, w := range strings.Fields(Normalize(s)) {
		if protectedCollectiveWords[w] {
			return true
		}
	}
	return false
}

// SplitUnderscoreCredit reports whether name is two or more person names
// joined by "_" ("Terry Pratchett_ Jacqueline Simpson", "Nick Kyme_Saul
// Reichlin"): a credit list, not filename shrapnel, and the author-split
// tooling's to split. A single name with an underscore ("Terry_Brooks",
// "J. N. Chaney_") is not one.
func SplitUnderscoreCredit(name string) ([]string, bool) {
	if !strings.Contains(name, "_") {
		return nil, false
	}
	var parts []string
	for _, p := range strings.Split(name, "_") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(strings.Fields(p)) < 2 || !personname.LooksLikePersonName(p) {
			return nil, false
		}
		parts = append(parts, p)
	}
	if len(parts) < 2 {
		return nil, false
	}
	return parts, true
}

var (
	// leadingMarkRe: "- Arthur C. Clarke", "+Brandon Sanderson".
	leadingMarkRe = regexp.MustCompile(`^\s*[-+]\s*(\S.*)$`)
	// copyrightPrefixRe: "(c) 2001 Stephen Hawking", "© 1994 Carl Sagan",
	// "Copyright 2001 X". The year is required: it is what makes the prefix a
	// copyright line and not a word.
	copyrightPrefixRe = regexp.MustCompile(`(?i)^\s*(?:\(c\)|©|copyright)\s*\d{4}\s+(\S.*)$`)
	// readBySuffixRe: "Christopher Paolini - Read by Gerard Doyle".
	readBySuffixRe = regexp.MustCompile(`(?i)^(.+?)\s+-\s+(?:read|narrated|performed)\s+by\s+\S.*$`)
	// studioBracketRe: "GraphicAudio [R. A. Salvatore]": a bracket after a
	// studio name, and nothing after it.
	studioBracketRe = regexp.MustCompile(`^\s*([^\[\]]+?)\s*\[([^\]]+)\]\s*$`)
	// byTailRe: "... by Saara El-Arifi_7": the last " by " and a tail with no
	// parenthesis, a trailing "_N" / number dropped.
	byTailRe = regexp.MustCompile(`(?i)^(.*\S)\s+by\s+([^()\[\]]+?)[\s_]*\d*[\s_]*$`)
)

// byRoleWords: the word before "by" that makes the tail someone other than
// the writer ("read by", "translated by").
var byRoleWords = map[string]bool{
	"read": true, "narrated": true, "performed": true, "translated": true,
	"edited": true, "illustrated": true, "adapted": true, "introduced": true,
	"compiled": true, "retold": true, "abridged": true, "foreword": true,
	"introduction": true, "afterword": true, "selected": true, "arranged": true,
	"produced": true, "directed": true, "presented": true, "hosted": true,
}

// CleanedName returns the person a junk author row's own name carries once
// its decoration is removed, and true, for exactly six shapes that carry a
// person:
//
//   - a bracketed name after a studio: "GraphicAudio [R. A. Salvatore]"
//     (several names in the bracket, "[Author / Narrator]": the FIRST only);
//   - a leading "-" or "+": "- Arthur C. Clarke", "+Brandon Sanderson";
//   - a copyright prefix with a year: "(c) 2001 Stephen Hawking";
//   - a narrator suffix: "Christopher Paolini - Read by Gerard Doyle";
//   - a person with a parenthetical that is not a studio or a narrator role:
//     "Dante King (Dragon Born)", "Daniel Pierce(Future Reborn)" (only junk
//     rows are cleaned, so "Kevin Hearne (Luke Daniels)", a narrator credit
//     the classifier leaves alone, never gets here);
//   - a writer's "by" tail: "Listening to Final Strife, The (The Final
//     Strife, Book 1) by Saara El-Arifi_7" (a trailing "_N" dropped; not
//     after "read", "translated" and the other role words in byRoleWords).
//
// Nothing else is cleaned. Stripping "_" or digits off a name yields titles
// and chapter labels as often as people ("HOR_ Prologue", "Killing Titan
// 01-44", "Country Mage_"), measured against every junk name in prod on
// 2026-09-29. The result must be person-shaped, not junk itself and not a
// credit list; the fixer further requires it to name an existing author
// with books, and not the book's title.
func CleanedName(name string) (string, bool) {
	s := strings.TrimSpace(name)
	if s == "" {
		return "", false
	}
	var out string
	switch {
	case studioBracketRe.MatchString(s):
		m := studioBracketRe.FindStringSubmatch(s)
		if classifyName(m[1]).Class != ClassPublisher {
			return "", false
		}
		out = m[2]
		if i := strings.IndexAny(out, "/;|"); i >= 0 {
			out = out[:i]
		}
	case leadingMarkRe.MatchString(s):
		out = leadingMarkRe.FindStringSubmatch(s)[1]
	case copyrightPrefixRe.MatchString(s):
		out = copyrightPrefixRe.FindStringSubmatch(s)[1]
	case readBySuffixRe.MatchString(s):
		out = readBySuffixRe.FindStringSubmatch(s)[1]
	case parenCleanable(s):
		out, _, _ = splitParenthetical(s)
	case byTailRe.MatchString(s):
		m := byTailRe.FindStringSubmatch(s)
		pre := strings.Fields(strings.ToLower(m[1]))
		if byRoleWords[strings.Trim(pre[len(pre)-1], ".,;:-()[]")] {
			return "", false
		}
		out = strings.TrimRight(m[2], "_ ")
	default:
		return "", false
	}
	out = strings.TrimSpace(spaceRe.ReplaceAllString(out, " "))
	if out == "" || out == s {
		return "", false
	}
	if !personname.LooksLikePersonName(out) || classifyName(out).Junk() || hasCreditSeparator(out) {
		return "", false
	}
	return out, true
}

// parenCleanable: "Head (Paren)" whose head is one person and whose
// parenthetical is neither a studio ("Brandon Sanderson (GraphicAudio)":
// the studio shape is left to the bracket form) nor a narrator role
// ("Michael Kramer (narrator)": the head is the reader).
func parenCleanable(s string) bool {
	head, paren, ok := splitParenthetical(s)
	if !ok || !personHead(head) || narratorRe.MatchString(s) {
		return false
	}
	pl := strings.ToLower(paren)
	if strings.Contains(pl, "narrat") || strings.Contains(pl, "read by") {
		return false
	}
	return classifyName(paren).Class != ClassPublisher
}

// hasCreditSeparator reports whether s joins several credits.
func hasCreditSeparator(s string) bool {
	l := strings.ToLower(s)
	return strings.ContainsAny(s, "&,;/+") || strings.Contains(l, " and ") || strings.Contains(l, " with ")
}

// leadsWithArticle: The/A/An as a WORD, not an initial ("A. Merritt") and not
// "A" before a single-letter word ("A J Finn").
func leadsWithArticle(s string) bool {
	f := strings.Fields(s)
	if len(f) < 2 {
		return false
	}
	switch strings.ToLower(f[0]) {
	case "the", "an":
		return true
	case "a":
		return len([]rune(strings.TrimRight(f[1], "."))) > 1
	}
	return false
}

// shoutWords counts words with no lowercase and at least two uppercase
// letters (author-path-link's authorPathLinkIsAllCapsWord).
func shoutWords(s string) int {
	n := 0
	for _, w := range strings.Fields(s) {
		upper, lower := 0, false
		for _, r := range w {
			if unicode.IsLower(r) {
				lower = true
				break
			}
			if unicode.IsUpper(r) {
				upper++
			}
		}
		if !lower && upper >= 2 {
			n++
		}
	}
	return n
}

func hasDigit(s string) bool { return strings.IndexFunc(s, unicode.IsDigit) >= 0 }

// singleToken: one word, e.g. a web-serial pen name ("nobody103"), which the
// filename-shape rule must not refuse.
func singleToken(s string) bool { return len(strings.Fields(s)) == 1 }
