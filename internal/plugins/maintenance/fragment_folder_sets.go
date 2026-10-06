// file: internal/plugins/maintenance/fragment_folder_sets.go
// version: 1.0.0
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
//     run of 1xx, 2xx numbers whose last two digits each restart is a disc
//     and its tracks (an explained gap); any other gap is held and listed
//     (skipped_chapter_gaps);
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
)

// Skip kinds of a folder chapter set.
const (
	fragSkipSetNumbering       = "skipped_set_numbering"
	fragSkipChapterGaps        = "skipped_chapter_gaps"
	fragSkipSetShort           = "skipped_set_too_short"
	fragSkipParentInFolder     = "skipped_parent_in_folder"
	fragSkipVersionGroupParent = "skipped_version_group_parent"
	fragSkipDuplicateAudio     = "skipped_duplicate_audio"
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
// leading or trailing " - " segment is dropped for. full is the title before
// that drop.
func fragSetTitle(stem string, sh fragNameShape, varying []int, authors []string) (title, full string) {
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
		s = s[:start] + " " + s[run[1]:]
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
	if fragSetTitleGeneric(title) {
		return "", ""
	}
	if fragSetTitleGeneric(full) {
		full = ""
	}
	return title, full
}

// fragSetTitleGeneric reports whether t names no work: no letters, or only a
// chapter word or edition note.
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
	return k == "" || fragGenericSetTitles[k]
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

// fragSetAuthors are the names a title's author segment is read against:
// the members' person-shaped author names and the set's folder and the
// folder above it when person-shaped.
func fragSetAuthors(lib *fragLibrary, dir string, cs []*fragCandidate) []string {
	var out []string
	add := func(a string) {
		a = strings.TrimSpace(a)
		if a != "" && personShapedName(a) && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	for _, c := range cs {
		add(lib.authorName(c.Book))
	}
	add(filepath.Base(dir))
	add(filepath.Base(filepath.Dir(dir)))
	return out
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
	authors := fragSetAuthors(lib, dir, sorted)
	set.title, set.altTitles = fragSetTitleOf(sorted[0].origStem(), shapes[0], varying, authors)
	if set.title == "" {
		base := filepath.Base(filepath.Clean(dir))
		for _, a := range append(authors, fragMemberAuthors(lib, sorted)...) {
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
			if g2, d2 := fragNumberGaps(hundreds); len(g2) == 0 {
				pairs, gaps, desc = hundreds, nil, d2
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
func fragSetTitleOf(stem string, sh fragNameShape, varying []int, authors []string) (string, []string) {
	t, full := fragSetTitle(stem, sh, varying, authors)
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
	groups := map[string][]*fragCandidate{}
	for _, c := range lone {
		sh, ok := fragFolderSetShape(c.origStem())
		if !ok {
			continue
		}
		dir, _ := groupDir(c)
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
		key := fragFolderSetKeyPrefix + name
		set := fragFolderSetOf(lib, dir, key, groups[gk])
		r := f.noParentRow(lib, dir, key, groups[gk], set)
		sets[r.RowID] = set
		rows = append(rows, r)
	}
	return rows, sets
}

// classifyFolderSets gives each folder chapter set row its class and the
// set's own holds, after the existing-book and same-audio checks have run.
// cands are every fragment candidate of this plan (siblings, not parents).
func (f *fragmentFixer) classifyFolderSets(lib *fragLibrary, live *fragLive, rows []repairs.Row, sets map[string]*fragFolderSet, cands []*fragCandidate) {
	if len(sets) == 0 {
		return
	}
	isCand := map[string]bool{}
	for _, c := range cands {
		isCand[c.Book.ID] = true
	}
	var dup *fragAudioIndex
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
			r.Evidence = append(r.Evidence, "formed as a folder chapter set (numbered files of one name in one folder); its work is already a live book, so it is never assembled")
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
		if r.Skipped != "" {
			continue
		}
		if why := folderSetParent(lib, live, r, set, isCand); why != "" {
			hold(fragSkipParentInFolder, why)
			continue
		}
		if why := folderSetVersionGroup(lib, r); why != "" {
			hold(fragSkipVersionGroupParent, why)
			continue
		}
		if dup == nil {
			dup = newFragAudioIndex(lib)
		}
		if why := dup.duplicates(r); why != "" {
			hold(fragSkipDuplicateAudio, why)
		}
	}
}

// folderSetEvidence adds the set's numbering and title lines.
func (f *fragmentFixer) folderSetEvidence(r *repairs.Row, set *fragFolderSet) {
	ev := fmt.Sprintf("folder chapter set: %d files share the name %q once their numbers are set aside", len(set.members), strings.TrimPrefix(set.key, fragFolderSetKeyPrefix))
	if set.numbering != "" {
		ev += fmt.Sprintf("; numbered %s", set.numbering)
	}
	switch {
	case len(set.gaps) > 0:
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
	lib       *fragLibrary
}

func newFragAudioIndex(lib *fragLibrary) *fragAudioIndex {
	ix := &fragAudioIndex{bySizeDur: map[[2]int64][]string{}, byHash: map[string][]string{}, lib: lib}
	for id, rows := range lib.files {
		if b, ok := lib.books[id]; !ok || b.SoftDeleted {
			continue
		}
		for _, r := range rows {
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

// duplicates names the row's files whose audio a live book outside the row
// also holds. "" none.
func (ix *fragAudioIndex) duplicates(r *repairs.Row) string {
	plan, ok := r.Detail.(*fragGroupPlan)
	if !ok {
		return ""
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
	var hits []string
	others := map[string]bool{}
	for _, c := range files {
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
	if len(hits) == 0 {
		return ""
	}
	return fmt.Sprintf("files of this set are audio %d other live book(s) already hold: %s; assembling would make a second copy of that audio; decide by hand",
		len(others), strings.Join(hits, "; "))
}
