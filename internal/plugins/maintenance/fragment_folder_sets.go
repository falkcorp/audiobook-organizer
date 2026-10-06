// file: internal/plugins/maintenance/fragment_folder_sets.go
// version: 1.3.0
// guid: 9f01b698-b911-4ecc-818e-6d10f415e768
// last-edited: 2026-10-05

// Folder chapter sets: the fragment-consolidation fixer's rule for a folder
// of numbered chapter files that share one name, that no parent book owns,
// and that the chapter-key groups left as lone chapters (owner decision
// 2026-10-05 19:30: "a folder of numbered chapter files with matching names,
// total length like a book, and no existing parent book becomes one new
// book"; plan and list only, the owner approves the row ids).
//
// WHY THE KEY GROUPS MISS THEM. metadata.ChapterGroupKey strips one chapter
// number, at the start or the end of a stem. "Jim Butcher - Turn Coat - 27
// 1" keeps "27" in its key (the trailing " 1" is the number it strips),
// "Metro 2034 - 03 Chapter 3" keeps "03", so every file of the folder has a
// key of its own and each is dropped as skipped_lone_chapter.
//
// THE RULE (fragFolderSetOf, Go and the review script share these cases in
// TestFragFolderSetShape):
//
//   - the name: the stem lower-cased, every ASCII digit run read as one
//     number slot ("#"), every run of characters that are neither letters
//     nor "#" read as one space. Files of one import folder (disc folders
//     count as one, groupDir) with the same name and at least fragMinGroup
//     of them are a candidate set;
//   - the numbering: the slots whose numbers differ across the files. One
//     slot is the chapter number. With two or more, the first slot whose
//     numbers are all different is ("Part 102-Chapter 2": 102), else two
//     slots read as a pair ("Blood Music 2-1"); anything else is held
//     (skipped_set_numbering). Numbers must run from 0 or 1 with no gap: a
//     run of 1xx, 2xx numbers is read as a disc and its tracks when that
//     explains the jumps (the jump is no gap; a missing track still is); any
//     gap is held and listed (skipped_chapter_gaps);
//   - the length: the kept chapters total at least fragFolderSetMinSec
//     (skipped_set_too_short), each still under the repairs' chapter limit
//     (the no-parent duration gate);
//   - the title: the first file's stem without its varying numbers, the
//     chapter word in front of each ("Chapter 3"), a duplicate suffix right
//     after the last one ("27 1"), and a leading or trailing " - " segment
//     that names the author ("Jim Butcher - Turn Coat" -> "Turn Coat"). A
//     stem that leaves nothing but a chapter word takes the folder's title,
//     unless the folder is named for the author (skipped_no_title_key);
//   - no existing book: the existing-book check (a live book of the title,
//     re-tagged copies) and the co-owner check run on these rows as on every
//     no-parent row. At plan time a set is also held when a live book that is
//     not a fragment candidate holds a file in the set's folders
//     (skipped_parent_in_folder), when a member's version group has a live
//     book outside the set (skipped_version_group_parent), or when a live
//     book outside the set holds a file of the same hash, or of the same size
//     and duration, as one of the set's (skipped_duplicate_audio);
//   - hands-off: a set the iTunes guard (or an iTunes persistent id) stops
//     is its own class, itunes-chapter-set, never applicable. Doctor Who /
//     Big Finish / Torchwood stays manual-only.
//
// PARENT SETS (owner decision 2026-10-05 20:45, "group by parent"). What is
// still lone after the folder sets is grouped by the folder ABOVE each
// file's import folder and the same name: the one-file-per-folder layout
// ("Joe Abercrombie/Before They Are Hanged 151 of 341/x.mp3"). The same tests
// apply, with three differences:
//
//   - a set whose work is already a live book is never held for it: when
//     the title matches and the totals agree (existingBookCheck), or when
//     one live book holds the audio of every held file and of at least half
//     the set (fragAudioIndex.joinTarget), it is parent-set-join-existing
//     and joins that book exactly as the existing-book class does (each
//     fragment retired into it with its listening state, keeping its own
//     file row). Audio held by several books, or by a chapter fragment, is
//     still held. The audio join applies to folder sets too (their class
//     stays existing-book);
//   - never one book of two: files of two known authors are held
//     (skipped_mixed_authors); two files at one position with different
//     audio are held (the no-parent track-order rule; same-size renamed
//     copies are retired as copies, as for every no-parent row); and two
//     sets that would each make a new book of one title (two parent folders
//     of one name and different lengths) are both held
//     (skipped_same_title_other_set);
//   - a library or import root is no work's parent and forms no set.
//
// APPLY is the no-parent row's: the row id is a no-parent id, Replan rebuilds
// it through replanGroup (which runs noParentRows on the row's own books, so
// every decision above that changes the plan reads only the members), and
// Apply moves the members' rows onto the survivor in this numbering's order,
// retitles it, retires the emptied fragments into it with their listening
// state (merge.FollowAbsorbedJournaled) and soft-deletes them; every step is
// journaled and revertable from the apply operation. The plan-time holds
// above read the whole library; a re-plan sees only the row's books, so they
// can only hold a row at plan time, never change one an apply re-checks.
package maintenance

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Classes of a folder chapter set's row.
const (
	// fragClassFolderSet: a folder chapter set with no parent (see the file
	// comment). Applicable when every test passes; never applied without
	// the owner's approval of its row id.
	fragClassFolderSet = "folder-chapter-set"
	// fragClassITunesSet: a folder chapter set under the iTunes library or
	// carrying an iTunes persistent id. Listed apart, never applicable.
	fragClassITunesSet = "itunes-chapter-set"
	// fragClassParentSet: a parent chapter set (owner decision 2026-10-05
	// 20:45, "group by parent"): one-file-per-folder chapters grouped by
	// the folder above their folders (".../Horizon Storms/Chapter 001/x.mp3"
	// is a chapter of "Horizon Storms"). Same tests as a folder set.
	fragClassParentSet = "parent-chapter-set"
	// fragClassParentJoin: a parent chapter set whose work is already a
	// live book (same title and total, or its audio held by that book): it
	// joins that book as the existing-book class does (each fragment
	// retired into it with its listening state, keeping its own file row),
	// never assembled into a second copy.
	fragClassParentJoin = "parent-set-join-existing"
)

// Skip kinds of a folder chapter set.
const (
	fragSkipSetNumbering       = "skipped_set_numbering"
	fragSkipChapterGaps        = "skipped_chapter_gaps"
	fragSkipSetShort           = "skipped_set_too_short"
	fragSkipParentInFolder     = "skipped_parent_in_folder"
	fragSkipVersionGroupParent = "skipped_version_group_parent"
	fragSkipDuplicateAudio     = "skipped_duplicate_audio"
	// fragSkipSameTitleSet: two chapter sets would each become a new book of
	// the same title (two parent folders of one name, of different
	// lengths): both are held, so no apply makes two books of one work.
	fragSkipSameTitleSet = "skipped_same_title_other_set"
	// fragSkipMixedAuthors: the set's files carry two different known
	// authors, so they may be two books.
	fragSkipMixedAuthors = "skipped_mixed_authors"
)

// fragFolderSetMinSec is the shortest total a folder chapter set is
// assembled at: one hour. Measured on prod 2026-10-05: 6.3% of the 9,055
// live primary multi-file books run under an hour (13.9% under two), and the
// candidate sets of book length ran 10.9-22.8 h, so any limit from one to
// ten hours selects the same sets; one hour keeps a short book eligible.
const fragFolderSetMinSec = 3600

// fragFolderSetKeyPrefix opens a folder chapter set's group key. Like
// fragNumberedKey it cannot collide with a chapter key (lower-case text).
const fragFolderSetKeyPrefix = "\x02set:"

// fragParentSetKeyPrefix opens a parent chapter set's group key.
const fragParentSetKeyPrefix = "\x03parent:"

// fragIsSetKey reports whether key is a folder or parent chapter set's.
func fragIsSetKey(key string) bool {
	return strings.HasPrefix(key, fragFolderSetKeyPrefix) || strings.HasPrefix(key, fragParentSetKeyPrefix)
}

// fragNameShape is a stem read as a name with number slots.
type fragNameShape struct {
	key  string   // the name: lower-case letters, "#" per number, single spaces
	runs [][2]int // byte offsets of each digit run in the stem
	nums []int
}

// fragFolderSetShape reads stem's name and numbers. ok is false for a stem
// with no number, or with a number of more than nine digits.
func fragFolderSetShape(stem string) (fragNameShape, bool) {
	var sh fragNameShape
	var sb strings.Builder
	space := false
	for i := 0; i < len(stem); {
		if c := stem[i]; c >= '0' && c <= '9' {
			j := i
			for j < len(stem) && stem[j] >= '0' && stem[j] <= '9' {
				j++
			}
			if j-i > 9 {
				return sh, false
			}
			n, err := strconv.Atoi(stem[i:j])
			if err != nil {
				return sh, false
			}
			sh.runs = append(sh.runs, [2]int{i, j})
			sh.nums = append(sh.nums, n)
			if space && sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			space = false
			sb.WriteByte('#')
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(stem[i:])
		i += size
		switch {
		case unicode.IsLetter(r) || r == '#':
			if space && sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			space = false
			sb.WriteString(strings.ToLower(string(r)))
		default:
			space = true
		}
	}
	sh.key = sb.String()
	return sh, len(sh.nums) > 0
}

// fragFolderSet is one folder chapter set's decision, computed from its
// members alone (so a re-plan computes the same one).
type fragFolderSet struct {
	// parent: grouped by the folder above the files' folders.
	parent  bool
	dir     string
	key     string
	members []*fragCandidate
	// pos is each member's chapter position by book id.
	pos map[string]metadata.ChapterPos
	// title is the work's title from the stems ("" none: the folder's
	// title is used, or none when noFolderTitle).
	title         string
	noFolderTitle bool
	// altTitles are further names the existing-book check reads (the title
	// before its author segment was dropped).
	altTitles []string
	// problem holds the set (skipped_set_numbering); gaps lists the missing
	// numbers (skipped_chapter_gaps); explained says why a gap is none.
	problem   string
	gaps      []string
	explained string
	// numbering describes the numbers for the evidence ("1-18").
	numbering string
}

// fragChapterWordTailRe is a chapter word right before a number ("Chapter ",
// "Part_", "pt.").
var fragChapterWordTailRe = regexp.MustCompile(`(?i)(?:^|[^a-z])((?:part|pt|chapter|chap|ch|track|trk|disc|disk|cd|episode|ep)\.?[\s_]*)$`)

// fragDupSuffixRe is what may follow the last varying number and is dropped
// with it: a short constant number ("27 1": a copy tool's suffix).
var fragDupSuffixRe = regexp.MustCompile(`^[\s_.\-]*\d{1,3}$`)

// fragOfTotalRe is an "of N" right after a varying number ("151 of 341"),
// removed with it.
var fragOfTotalRe = regexp.MustCompile(`(?i)^\s*(?:of|/)\s*\d+`)

// fragDashRunRe collapses the dashes a removed number leaves ("A -  - B").
var fragDashRunRe = regexp.MustCompile(`\s*-\s*(?:-\s*)+`)

// fragEmptyBracketsRe is a bracket pair a removed number left empty
// ("Title (Part 2)" -> "Title ( )").
var fragEmptyBracketsRe = regexp.MustCompile(`\(\s*\)|\[\s*\]|\{\s*\}`)

// fragTrailChapterWordRe is a chapter word left at the end without its
// number.
var fragTrailChapterWordRe = regexp.MustCompile(`(?i)(?:^|[\s\-_.,]+)(?:part|pt|chapter|chap|ch|track|trk|disc|disk|cd|episode|ep)\.?$`)

// fragGenericSetTitles are names that are no work's title.
var fragGenericSetTitles = map[string]bool{
	"chapter": true, "chap": true, "ch": true, "part": true, "pt": true, "track": true, "trk": true,
	"disc": true, "disk": true, "cd": true, "episode": true, "ep": true, "audiobook": true,
	"unabridged": true, "abridged": true, "read by narrator": true, "copy": true,
}

// fragSetTitle is a set's title from one member's stem: varying holds the
// slot indexes whose numbers differ across the set, authors the names a
// leading or trailing " - " segment is dropped for, people the names a
// whole title must not be (it then names no work). full is the title before
// the segment drop.
func fragSetTitle(stem string, sh fragNameShape, varying []int, authors, people []string) (title, full string) {
	if len(varying) == 0 {
		return "", ""
	}
	s := stem
	last := sh.runs[varying[len(varying)-1]]
	if fragDupSuffixRe.MatchString(s[last[1]:]) {
		s = s[:last[1]]
	}
	for i := len(varying) - 1; i >= 0; i-- {
		run := sh.runs[varying[i]]
		start := run[0]
		if m := fragChapterWordTailRe.FindStringSubmatchIndex(s[:start]); m != nil {
			start = m[2]
		}
		end := run[1]
		if m := fragOfTotalRe.FindStringIndex(s[end:]); m != nil {
			end += m[1]
		}
		s = s[:start] + " " + s[end:]
	}
	clean := func(t string) string {
		t = strings.ReplaceAll(t, "_", " ")
		t = fragEmptyBracketsRe.ReplaceAllString(t, " ")
		t = strings.Join(strings.Fields(t), " ")
		t = fragDashRunRe.ReplaceAllString(t, " - ")
		for {
			u := strings.Trim(t, " -_.,:;")
			u = fragTrailChapterWordRe.ReplaceAllString(u, "")
			if u == t {
				return t
			}
			t = u
		}
	}
	s = clean(s)
	full = s
	segs := strings.Split(s, " - ")
	names := func(seg string) bool {
		for _, a := range authors {
			if folderNamesAuthor(seg, a) {
				return true
			}
		}
		return false
	}
	if len(segs) >= 2 && names(segs[0]) {
		segs = segs[1:]
	}
	if len(segs) >= 2 && names(segs[len(segs)-1]) {
		segs = segs[:len(segs)-1]
	}
	title = clean(strings.Join(segs, " - "))
	isPerson := false
	for _, a := range people {
		isPerson = isPerson || folderNamesAuthor(title, a)
	}
	if fragSetTitleGeneric(title) || isPerson {
		// Nothing but a chapter word, or nothing but the author's name
		// ("195-299 Kevin J Anderson"): the folder names the work.
		return "", ""
	}
	if fragSetTitleGeneric(full) {
		full = ""
	}
	return title, full
}

// fragSetTitleGeneric reports whether t names no work: no letters, only a
// chapter word or edition note, or a placeholder ("Unknown Author").
func fragSetTitleGeneric(t string) bool {
	var sb strings.Builder
	space := false
	for _, r := range strings.ToLower(t) {
		if unicode.IsLetter(r) {
			if space && sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			space = false
			sb.WriteRune(r)
		} else {
			space = true
		}
	}
	k := sb.String()
	return k == "" || fragGenericSetTitles[k] || authorname.IsPlaceholderAuthor(strings.TrimSpace(t)) ||
		metadata.IsGenericDirName(t)
}

// fragVaryingSlots lists the slots whose numbers differ across shapes (all
// of one name, so of one slot count).
func fragVaryingSlots(shapes []fragNameShape) []int {
	var v []int
	for i := range shapes[0].nums {
		for _, sh := range shapes[1:] {
			if sh.nums[i] != shapes[0].nums[i] {
				v = append(v, i)
				break
			}
		}
	}
	return v
}

// fragSetAuthors are the names a title's " - " author segment is read
// against (authors) and the names a whole title or the set's folder must not
// be (people). people: the members' person-shaped author names and the
// folder above the set's folder when person-shaped. authors adds the set's
// own folder when person-shaped ("iTunes Media/Audiobooks/Jim Butcher"):
// it may name the author, but it may as well name the work ("Christopher
// Paolini/Some Work"), so it never empties a title on its own.
func fragSetAuthors(lib *fragLibrary, dir string, cs []*fragCandidate) (authors, people []string) {
	add := func(list []string, a string) []string {
		a = strings.TrimSpace(a)
		if a != "" && personShapedName(a) && !slices.Contains(list, a) {
			list = append(list, a)
		}
		return list
	}
	for _, c := range cs {
		people = add(people, lib.authorName(c.Book))
	}
	people = add(people, filepath.Base(filepath.Dir(dir)))
	authors = add(append([]string(nil), people...), filepath.Base(dir))
	return authors, people
}

// fragFolderSetOf decides one candidate set: dir and key its group, cs its
// files (all of the name key).
func fragFolderSetOf(lib *fragLibrary, dir, key string, cs []*fragCandidate) *fragFolderSet {
	set := &fragFolderSet{dir: dir, key: key, members: cs, pos: map[string]metadata.ChapterPos{}}
	sorted := append([]*fragCandidate(nil), cs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].origStem() != sorted[j].origStem() {
			return sorted[i].origStem() < sorted[j].origStem()
		}
		return sorted[i].Book.ID < sorted[j].Book.ID
	})
	shapes := make([]fragNameShape, len(sorted))
	discs := make([]int, len(sorted))
	for i, c := range sorted {
		shapes[i], _ = fragFolderSetShape(c.origStem())
		_, discs[i] = groupDir(c)
	}
	varying := fragVaryingSlots(shapes)
	authors, people := fragSetAuthors(lib, dir, sorted)
	set.title, set.altTitles = fragSetTitleOf(sorted[0].origStem(), shapes[0], varying, authors, people)
	if set.title == "" {
		base := filepath.Base(filepath.Clean(dir))
		for _, a := range append(people, fragMemberAuthors(lib, sorted)...) {
			if folderNamesAuthor(base, a) {
				set.noFolderTitle = true
			}
		}
	}
	// The counter: one varying slot; else the first whose numbers are all
	// different; else two slots read as a pair.
	pairs := make([][2]int, len(sorted))
	switch {
	case len(varying) == 0:
		set.problem = fmt.Sprintf("every file carries the same numbers (%q): no chapter sequence to order them by", sorted[0].origStem())
		return set
	case len(varying) == 1:
		for i := range sorted {
			pairs[i] = [2]int{discs[i], shapes[i].nums[varying[0]]}
		}
	default:
		slot := -1
		for _, v := range varying {
			seen := map[[2]int]bool{}
			distinct := true
			for i := range sorted {
				k := [2]int{discs[i], shapes[i].nums[v]}
				if seen[k] {
					distinct = false
					break
				}
				seen[k] = true
			}
			if distinct {
				slot = v
				break
			}
		}
		switch {
		case slot >= 0:
			for i := range sorted {
				pairs[i] = [2]int{discs[i], shapes[i].nums[slot]}
			}
		case len(varying) == 2 && !slices.ContainsFunc(discs, func(d int) bool { return d > 0 }):
			seen := map[[2]int]bool{}
			for i := range sorted {
				k := [2]int{shapes[i].nums[varying[0]], shapes[i].nums[varying[1]]}
				if seen[k] {
					set.problem = fmt.Sprintf("two files carry the numbers %d-%d: no counter orders the set", k[0], k[1])
					return set
				}
				seen[k] = true
				pairs[i] = k
			}
		default:
			set.problem = fmt.Sprintf("%d numbers vary across the files and none counts them alone (%q … %q)",
				len(varying), sorted[0].origStem(), sorted[len(sorted)-1].origStem())
			return set
		}
	}
	gaps, desc := fragNumberGaps(pairs)
	if len(gaps) > 0 && !slices.ContainsFunc(pairs, func(p [2]int) bool { return p[0] != 0 }) {
		// 101-112, 201-210: a disc in the hundreds and its tracks.
		hundreds := make([][2]int, len(pairs))
		ok := true
		for i, p := range pairs {
			if p[1] < 100 {
				ok = false
				break
			}
			hundreds[i] = [2]int{p[1] / 100, p[1] % 100}
		}
		if ok {
			// Read as discs when that explains the jumps: no gap left, or
			// fewer than the flat reading leaves (then the rest are listed
			// as disc-track gaps, "1-01").
			if g2, d2 := fragNumberGaps(hundreds); len(g2) < len(gaps) {
				pairs, gaps, desc = hundreds, g2, d2
				set.explained = "numbered by disc in the hundreds (1xx, 2xx …): the jump between discs is no gap"
			}
		}
	}
	set.gaps, set.numbering = gaps, desc
	for i, c := range sorted {
		p := pairs[i]
		if p[0] == 0 {
			set.pos[c.Book.ID] = metadata.ChapterPos{Parts: []int{p[1]}}
		} else {
			set.pos[c.Book.ID] = metadata.ChapterPos{Parts: []int{p[0], p[1]}}
		}
	}
	return set
}

// fragSetTitleOf is fragSetTitle with the full title as an alternative name
// when it differs.
func fragSetTitleOf(stem string, sh fragNameShape, varying []int, authors, people []string) (string, []string) {
	t, full := fragSetTitle(stem, sh, varying, authors, people)
	var alt []string
	if full != "" && full != t {
		alt = append(alt, full)
	}
	return t, alt
}

// fragMemberAuthors are the members' author names, person-shaped or not.
func fragMemberAuthors(lib *fragLibrary, cs []*fragCandidate) []string {
	var out []string
	for _, c := range cs {
		if a := strings.TrimSpace(lib.authorName(c.Book)); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// fragNumberGaps checks positions (major, minor) for gaps: the majors (when
// more than one) run on from 0 or 1; the first major's minors run from 0 or
// 1, each later major's from 0, 1 or on from the major before. A repeated
// position is no gap (the copies rule decides it). It returns the missing
// positions and a description of the numbers.
func fragNumberGaps(pairs [][2]int) (missing []string, desc string) {
	byMajor := map[int][]int{}
	var majors []int
	for _, p := range pairs {
		if _, ok := byMajor[p[0]]; !ok {
			majors = append(majors, p[0])
		}
		byMajor[p[0]] = append(byMajor[p[0]], p[1])
	}
	sort.Ints(majors)
	two := len(majors) > 1 || majors[0] != 0
	name := func(maj, mn int) string {
		if two {
			return fmt.Sprintf("%d-%02d", maj, mn)
		}
		return strconv.Itoa(mn)
	}
	if len(majors) > 1 {
		if majors[0] > 1 {
			for m := 1; m < majors[0]; m++ {
				missing = append(missing, fmt.Sprintf("disc %d", m))
			}
		}
		for i := 1; i < len(majors); i++ {
			for m := majors[i-1] + 1; m < majors[i]; m++ {
				missing = append(missing, fmt.Sprintf("disc %d", m))
			}
		}
	}
	var parts []string
	prevLast := -1
	for i, maj := range majors {
		ns := slices.Compact(slices.Sorted(slices.Values(byMajor[maj])))
		first := ns[0]
		switch {
		case first <= 1:
		case i > 0 && first == prevLast+1:
		default:
			// Neither a restart nor a run on: named as a restart's gap.
			for n := 1; n < first; n++ {
				missing = append(missing, name(maj, n))
			}
		}
		for k := 1; k < len(ns); k++ {
			for n := ns[k-1] + 1; n < ns[k]; n++ {
				missing = append(missing, name(maj, n))
			}
		}
		prevLast = ns[len(ns)-1]
		parts = append(parts, name(maj, first)+"–"+name(maj, prevLast))
	}
	return missing, strings.Join(parts, ", ")
}

// fragFormatGaps renders missing positions, at most 15 named.
func fragFormatGaps(gaps []string) string {
	if len(gaps) <= 15 {
		return strings.Join(gaps, ", ")
	}
	return strings.Join(gaps[:15], ", ") + fmt.Sprintf(" … and %d more", len(gaps)-15)
}

// folderSetRows forms the folder chapter sets from the lone chapters (the
// fragments the key groups dropped as skipped_lone_chapter): per import
// folder and name, fragMinGroup files or more. Each becomes a no-parent row
// (noParentRow with the set's positions and title). It returns the rows and
// each row's set by row id; the members of every row are placed.
func (f *fragmentFixer) folderSetRows(lib *fragLibrary, lone []*fragCandidate) ([]repairs.Row, map[string]*fragFolderSet) {
	return f.setRows(lib, lone, false)
}

// parentSetRows forms the parent chapter sets from the lone chapters no
// folder set took: per folder ABOVE the import folder and name, fragMinGroup
// files or more (".../Horizon Storms/Chapter 001/x.mp3" and ".../Horizon
// Storms/Chapter 002/y.mp3" are one set under "Horizon Storms"). A folder
// that is a library or import root is no work's parent and forms none.
func (f *fragmentFixer) parentSetRows(lib *fragLibrary, lone []*fragCandidate) ([]repairs.Row, map[string]*fragFolderSet) {
	return f.setRows(lib, lone, true)
}

func (f *fragmentFixer) setRows(lib *fragLibrary, lone []*fragCandidate, parent bool) ([]repairs.Row, map[string]*fragFolderSet) {
	prefix := fragFolderSetKeyPrefix
	if parent {
		prefix = fragParentSetKeyPrefix
	}
	groups := map[string][]*fragCandidate{}
	for _, c := range lone {
		sh, ok := fragFolderSetShape(c.origStem())
		if !ok {
			continue
		}
		dir, _ := groupDir(c)
		if parent {
			dir = filepath.Dir(filepath.Clean(dir))
			if slices.Contains(lib.roots, dir) || dir == lib.libraryRoot || dir == filepath.Dir(dir) {
				continue
			}
		}
		gk := dir + "\x00" + sh.key
		groups[gk] = append(groups[gk], c)
	}
	keys := make([]string, 0, len(groups))
	for k, cs := range groups {
		if len(cs) >= fragMinGroup {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var rows []repairs.Row
	sets := map[string]*fragFolderSet{}
	for _, gk := range keys {
		dir, name, _ := strings.Cut(gk, "\x00")
		key := prefix + name
		set := fragFolderSetOf(lib, dir, key, groups[gk])
		set.parent = parent
		r := f.noParentRow(lib, dir, key, groups[gk], set)
		sets[r.RowID] = set
		rows = append(rows, r)
	}
	return rows, sets
}

// classifyFolderSets gives each folder or parent chapter set row its class
// and the set's own holds, after the existing-book and same-audio checks
// have run. cands are every fragment candidate of this plan (siblings, not
// parents). In order, for a set no earlier test held:
//
//  1. two known authors among the files: held (never one book of two);
//  2. its audio is an existing book's (fragAudioIndex.joinTarget): joined
//     into that book (joinExisting, the existing-book class's join);
//  3. numbering, gaps, length, title;
//  4. a live book in the set's folders or a member's version group;
//
// and finally, across the rows, two sets that would each make a new book of
// one title are both held.
func (f *fragmentFixer) classifyFolderSets(lib *fragLibrary, live *fragLive, rows []repairs.Row, sets map[string]*fragFolderSet, cands []*fragCandidate) {
	if len(sets) == 0 {
		return
	}
	isCand := map[string]bool{}
	for _, c := range cands {
		isCand[c.Book.ID] = true
	}
	var dup *fragAudioIndex
	byTitle := map[string][]int{}
	for i := range rows {
		r := &rows[i]
		set, ok := sets[r.RowID]
		if !ok {
			// An existing-book row keeps its group's hash after the prefix.
			_, h, _ := strings.Cut(r.RowID, ":")
			set, ok = sets["no-parent:"+h]
			if !ok || r.Class != fragClassExistingBook {
				continue
			}
			f.folderSetEvidence(r, set)
			r.Evidence = append(r.Evidence, "its work is already a live book (same title, total agreeing), so it joins that book and is never assembled")
			if set.parent {
				r.Class = fragClassParentJoin
			}
			continue
		}
		f.folderSetEvidence(r, set)
		switch r.Class {
		case fragClassManual:
			if r.Skipped == repairs.SkipITunes {
				r.Class = fragClassITunesSet
			}
			continue
		case fragClassNoParent:
			r.Class = fragClassFolderSet
			if set.parent {
				r.Class = fragClassParentSet
			}
		default:
			continue
		}
		if r.Skipped != "" {
			continue
		}
		hold := func(kind, why string) {
			r.Risk, r.Skipped, r.SkipReason = repairs.RiskReview, kind, why
		}
		plan, _ := r.Detail.(*fragGroupPlan)
		if why := fragMixedAuthors(lib, plan); why != "" {
			hold(fragSkipMixedAuthors, why)
			continue
		}
		if dup == nil {
			dup = newFragAudioIndex(lib)
		}
		target, why := dup.joinTarget(r)
		switch {
		case target != "" && isCand[target]:
			hold(fragSkipDuplicateAudio, why+"; that book is itself a chapter fragment, not a book to join")
			continue
		case target != "" && lib.assembled[target]:
			hold(fragSkipDuplicateAudio, why+"; that book was itself assembled by an earlier no-parent apply of this fixer (a possible second copy); revert that apply first")
			continue
		case target != "":
			parent := set.parent
			tb := lib.books[target]
			setAuthor, bookAuthor := groupAuthor(lib, plan), lib.authorName(tb)
			e := fragExisting{id: target}
			e.total, e.unknown, e.files, e.missing = lib.joinTotal(target)
			total, _ := rowFragTotal(r)
			authorWord := "they agree"
			switch {
			case fragAuthorsDiffer(setAuthor, bookAuthor):
				hold(fragSkipDuplicateAudio, fmt.Sprintf("%s; but the set's author %q and book %s's author %q differ, so the match is not trusted; decide by hand",
					why, setAuthor, target, bookAuthor))
				continue
			case strings.TrimSpace(setAuthor) == "" || strings.TrimSpace(bookAuthor) == "" ||
				authorname.IsPlaceholderAuthor(setAuthor) || authorname.IsPlaceholderAuthor(bookAuthor):
				authorWord = "one side has no known author"
			}
			files := len(plan.Members) + len(plan.Copies)
			e.audio = fmt.Sprintf("audio join (not a title match): all %d of the set's %d files' audio is held by book %s (%q, %d file(s)); totals: set %s, book %s; titles: set %q, book %q; authors: set %q, book %q (%s); %s",
				files, files, target, tb.Title, e.files, fragHours(total), fragHours(e.total), plan.Title, tb.Title,
				setAuthor, bookAuthor, authorWord, why)
			if e.missing > 0 {
				e.audio += fmt.Sprintf("; book %s also has %d row(s) whose file is missing (not among the matched files, not counted in its total)", target, e.missing)
			}
			f.joinExisting(lib, r, plan, e, "", func(e fragExisting) string {
				return fmt.Sprintf("book %s (%q, %d file(s), %s)", e.id, lib.books[e.id].Title, e.files, fragHours(e.total))
			}, nil)
			if r.Class == fragClassExistingBook && parent {
				r.Class = fragClassParentJoin
			}
			continue
		case why != "":
			hold(fragSkipDuplicateAudio, why+"; it is not one existing book's audio, so neither assembling nor joining is proven; decide by hand")
			continue
		}
		total := 0
		for _, m := range plan.Members {
			total += m.Frag.File.Duration
		}
		switch {
		case set.problem != "":
			hold(fragSkipSetNumbering, set.problem)
		case len(set.gaps) > 0:
			hold(fragSkipChapterGaps, fmt.Sprintf("the chapter numbers (%s) have gaps: %s missing; the missing chapters may be elsewhere in the library, so the owner decides",
				set.numbering, fragFormatGaps(set.gaps)))
		case total < fragFolderSetMinSec:
			hold(fragSkipSetShort, fmt.Sprintf("the %d chapters total %s, under the %d min a book-length set needs", len(plan.Members), fragHours(total), fragFolderSetMinSec/60))
		case plan.Title == "":
			hold(fragSkipNoTitleKey, "neither the file names nor the folder give the work's title (the folder is named for the author or is generic)")
		}
		if plan.Title != "" {
			if k := fragTitleKey(plan.Title); k != "" {
				byTitle[k] = append(byTitle[k], i)
			}
		}
		if r.Skipped != "" {
			continue
		}
		if why := folderSetParent(lib, live, r, set, isCand); why != "" {
			hold(fragSkipParentInFolder, why)
			continue
		}
		if why := folderSetVersionGroup(lib, r); why != "" {
			hold(fragSkipVersionGroupParent, why)
		}
	}
	// Never two new books of one work: sets that would each assemble a book
	// of the same title are all held (plan time; a re-plan sees one row).
	for _, idx := range byTitle {
		if len(idx) < 2 {
			continue
		}
		var ids []string
		for _, i := range idx {
			ids = append(ids, rows[i].RowID)
		}
		for _, i := range idx {
			r := &rows[i]
			why := fmt.Sprintf("%d chapter sets would each become a new book titled %q (rows %s): they may be one work in two places or two editions; decide which, if any, to assemble",
				len(idx), r.Proposed["title"], strings.Join(ids, ", "))
			r.Evidence = append(r.Evidence, why)
			if r.Skipped == "" {
				r.Risk, r.Skipped, r.SkipReason = repairs.RiskReview, fragSkipSameTitleSet, why
			}
		}
	}
}

// fragMixedAuthors names two different known authors among the plan's
// files. "" none.
func fragMixedAuthors(lib *fragLibrary, plan *fragGroupPlan) string {
	var seen []string
	for _, m := range plan.Members {
		a := lib.authorName(m.Frag.Book)
		if a == "" {
			continue
		}
		for _, b := range seen {
			if fragAuthorsDiffer(a, b) {
				return fmt.Sprintf("the files carry two different authors (%q and %q): they may be two books, so they are not assembled into one", b, a)
			}
		}
		if !slices.Contains(seen, a) {
			seen = append(seen, a)
		}
	}
	return ""
}

// folderSetEvidence adds the set's numbering and title lines.
func (f *fragmentFixer) folderSetEvidence(r *repairs.Row, set *fragFolderSet) {
	ev := fmt.Sprintf("folder chapter set: %d files share the name %q once their numbers are set aside", len(set.members), strings.TrimPrefix(set.key, fragFolderSetKeyPrefix))
	if set.numbering != "" {
		ev += fmt.Sprintf("; numbered %s", set.numbering)
	}
	switch {
	case len(set.gaps) > 0:
		if set.explained != "" {
			ev += "; " + set.explained
		}
		ev += fmt.Sprintf("; gaps: %s missing", fragFormatGaps(set.gaps))
	case set.explained != "":
		ev += "; " + set.explained
	case set.problem == "":
		ev += "; no gaps"
	}
	if set.title != "" {
		ev += fmt.Sprintf("; title %q from the file names", set.title)
	} else if !set.noFolderTitle {
		ev += "; title from the folder (the file names give only a chapter word)"
	}
	r.Evidence = append(r.Evidence, ev)
}

// folderSetParent names a live book that is not a fragment candidate and
// holds a file in one of the set's folders (the import folder, each member's
// import and current folder): a book that may be the set's parent. "" none.
func folderSetParent(lib *fragLibrary, live *fragLive, r *repairs.Row, set *fragFolderSet, isCand map[string]bool) string {
	in := map[string]bool{}
	for _, id := range r.BookIDs {
		in[id] = true
	}
	folders := map[string]bool{filepath.Clean(set.dir): true}
	for _, c := range set.members {
		folders[filepath.Dir(c.File.Path)] = true
		if c.ImportPath != "" {
			folders[filepath.Dir(c.ImportPath)] = true
		}
	}
	var found []string
	for _, d := range sortedKeys(folders) {
		for _, id := range live.byDir[d] {
			if in[id] || isCand[id] || slices.Contains(found, id) {
				continue
			}
			found = append(found, id)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	var ds []string
	for _, id := range found {
		if len(ds) == 5 {
			ds = append(ds, fmt.Sprintf("… and %d more", len(found)-5))
			break
		}
		total, _, n := lib.bookTotal(id)
		ds = append(ds, fmt.Sprintf("%s (%q, %d file(s), %s)", id, lib.books[id].Title, n, fragHours(total)))
	}
	return "a live book that is not a chapter fragment holds files in this set's folder: " + strings.Join(ds, "; ") +
		"; it may be the set's parent, so the set is not assembled beside it; decide by hand"
}

// folderSetVersionGroup names a live book outside the row in a member's
// version group: another version of the work already exists. "" none.
func folderSetVersionGroup(lib *fragLibrary, r *repairs.Row) string {
	in := map[string]bool{}
	groups := map[string]bool{}
	for _, id := range r.BookIDs {
		in[id] = true
		if g := lib.books[id].VersionGroup; g != "" {
			groups[g] = true
		}
	}
	if len(groups) == 0 {
		return ""
	}
	var found []string
	for id, b := range lib.books {
		if !in[id] && !b.SoftDeleted && groups[b.VersionGroup] {
			found = append(found, id)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	b := lib.books[found[0]]
	return fmt.Sprintf("a member's version group holds %d live book(s) outside this set (first %s, %q, group %s): another version of the work exists; decide by hand",
		len(found), found[0], b.Title, b.VersionGroup)
}

// fragAudioIndex finds live books holding a file of the same hash, or of the
// same size and duration, as another.
type fragAudioIndex struct {
	bySizeDur map[[2]int64][]string
	byHash    map[string][]string
	// goneBySizeDur / goneByHash index the rows whose file is missing: never
	// a join's evidence, but a set matching them is held (its fragments may
	// be the only copies of that book's audio on disk; a repoint, not a
	// join, is the repair).
	goneBySizeDur map[[2]int64][]string
	goneByHash    map[string][]string
	lib           *fragLibrary
}

func newFragAudioIndex(lib *fragLibrary) *fragAudioIndex {
	ix := &fragAudioIndex{bySizeDur: map[[2]int64][]string{}, byHash: map[string][]string{},
		goneBySizeDur: map[[2]int64][]string{}, goneByHash: map[string][]string{}, lib: lib}
	for id, rows := range lib.files {
		if b, ok := lib.books[id]; !ok || b.SoftDeleted {
			continue
		}
		for _, r := range rows {
			if r.Missing {
				// A missing file is no audio the book holds: a set is never
				// joined into a book on the strength of files that are gone
				// (the set's fragments may be the only copies on disk).
				if r.Size > 0 && r.Duration > 0 {
					k := [2]int64{r.Size, int64(r.Duration)}
					ix.goneBySizeDur[k] = append(ix.goneBySizeDur[k], id)
				}
				if r.Hash != "" {
					ix.goneByHash[r.Hash] = append(ix.goneByHash[r.Hash], id)
				}
				continue
			}
			if r.Size > 0 && r.Duration > 0 {
				k := [2]int64{r.Size, int64(r.Duration)}
				ix.bySizeDur[k] = append(ix.bySizeDur[k], id)
			}
			if r.Hash != "" {
				ix.byHash[r.Hash] = append(ix.byHash[r.Hash], id)
			}
		}
	}
	return ix
}

// goneOwners names the live books outside in with a MISSING row matching
// c's file by hash, or by size and duration.
func (ix *fragAudioIndex) goneOwners(c *fragCandidate, in map[string]bool) []string {
	var out []string
	if c.File.Hash != "" {
		for _, id := range ix.goneByHash[c.File.Hash] {
			if !in[id] {
				out = append(out, id)
			}
		}
	}
	if len(out) == 0 && c.File.Size > 0 && c.File.Duration > 0 {
		for _, id := range ix.goneBySizeDur[[2]int64{c.File.Size, int64(c.File.Duration)}] {
			if !in[id] {
				out = append(out, id)
			}
		}
	}
	return uniqueSorted(out)
}

// joinTarget reads the row's files against the live books outside it: the
// one book that holds the audio of EVERY file of the row, members and
// copies (target, with why), or no target and why when some files are held
// but not all, or by more than one book. Both "" when no file's audio is
// held elsewhere. A join retires every fragment into the target, so a
// fragment whose audio the target does not hold must never be in one.
func (ix *fragAudioIndex) joinTarget(r *repairs.Row) (target, why string) {
	plan, ok := r.Detail.(*fragGroupPlan)
	if !ok {
		return "", ""
	}
	in := map[string]bool{}
	for _, id := range r.BookIDs {
		in[id] = true
	}
	var files []*fragCandidate
	for _, m := range plan.Members {
		files = append(files, m.Frag)
	}
	for _, cp := range plan.Copies {
		files = append(files, cp.Frag)
	}
	var hits, gone []string
	held := 0
	others := map[string]bool{}
	for _, c := range files {
		if gb := ix.goneOwners(c, in); len(gb) > 0 && len(gone) < 5 {
			gone = append(gone, fmt.Sprintf("%q (book %s)", c.origStem(), strings.Join(gb, ", ")))
		}
		var owners []string
		how := ""
		if c.File.Hash != "" {
			for _, id := range ix.byHash[c.File.Hash] {
				if !in[id] {
					owners, how = append(owners, id), "same hash"
				}
			}
		}
		if len(owners) == 0 && c.File.Size > 0 && c.File.Duration > 0 {
			for _, id := range ix.bySizeDur[[2]int64{c.File.Size, int64(c.File.Duration)}] {
				if !in[id] {
					owners, how = append(owners, id), "same size and duration"
				}
			}
		}
		if len(owners) == 0 {
			continue
		}
		held++
		owners = uniqueSorted(owners)
		for _, id := range owners {
			others[id] = true
		}
		if len(hits) < 5 {
			hits = append(hits, fmt.Sprintf("%q (%s as book %s)", c.origStem(), how, strings.Join(owners, ", ")))
		} else if len(hits) == 5 {
			hits = append(hits, "…")
		}
	}
	if len(gone) > 0 {
		// Held whatever else matched: the matching book rows' files are gone,
		// so these fragments may be the only copies of that audio.
		why = "files of the set match rows of a live book whose files are missing on disk: " + strings.Join(gone, "; ") +
			"; these fragments may be that book's only copies, so the set is neither joined nor assembled (a repoint is the repair); decide by hand"
		if held > 0 {
			why += fmt.Sprintf("; %d file(s) also match present rows: %s", held, strings.Join(hits, "; "))
		}
		return "", why
	}
	if held == 0 {
		return "", ""
	}
	why = fmt.Sprintf("%d of the set's %d files are audio %d other live book(s) already hold: %s",
		held, len(files), len(others), strings.Join(hits, "; "))
	if len(others) == 1 && held == len(files) {
		for id := range others {
			return id, why
		}
	}
	if len(others) == 1 {
		// One book holds only part of the set: joining would retire the
		// other fragments into a book that does not hold their audio, so
		// that audio would sit in no live book. The whole set is held.
		why += fmt.Sprintf("; the other %d file(s)' audio is in no other book, so the set is not that book's copy", len(files)-held)
	}
	return "", why
}
