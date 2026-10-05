// file: internal/plugins/maintenance/audible_read_status_match.go
// version: 1.1.0
// guid: 4fe74359-29db-4163-a16f-6d8b17834356
// last-edited: 2026-10-05

package maintenance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// This file is the input side of maintenance.audible-read-status: the
// Audible library export it reads (lenient about the shapes the export
// tools write) and the library index it matches the export against. The
// decisions and the Repairs-lane contract are in audible_read_status_fixer.go.

// arsParams are the fixer's plan params. The export goes in the plan request
// body: {"target_user_id": "...", "items": [...]}, or the export file as is
// under "export" ({"export": {"items": [...]}}). Only the fields arsItem
// names are read; strip the rest before posting to stay under the API's JSON
// body limit.
type arsParams struct {
	// TargetUserID is the user whose listening state is written. Required
	// and never defaulted (not to the caller, not to anyone).
	TargetUserID string    `json:"target_user_id"`
	Items        []arsItem `json:"items,omitempty"`
	Export       *struct {
		Items []arsItem `json:"items"`
	} `json:"export,omitempty"`
}

func (p arsParams) items() []arsItem {
	if len(p.Items) > 0 || p.Export == nil {
		return p.Items
	}
	return p.Export.Items
}

// arsItem is one title of the Audible export, compact: it is also what each
// planned row stores (Row.State), so a re-plan decides from exactly the item
// the plan saw.
type arsItem struct {
	ASIN            string        `json:"asin,omitempty"`
	Title           string        `json:"title,omitempty"`
	Authors         arsNames      `json:"authors,omitempty"`
	Narrators       arsNames      `json:"narrators,omitempty"`
	RuntimeMin      arsNum        `json:"runtime_length_min,omitempty"`
	IsFinished      *arsBool      `json:"is_finished,omitempty"`
	PercentComplete arsNum        `json:"percent_complete,omitempty"`
	Listening       *arsListening `json:"listening_status,omitempty"`
}

// arsListening is the export's listening_status object.
type arsListening struct {
	FinishedAt      string   `json:"finished_at_timestamp,omitempty"`
	TimeRemaining   *arsNum  `json:"time_remaining_seconds,omitempty"`
	IsFinished      *arsBool `json:"is_finished,omitempty"`
	PercentComplete *arsNum  `json:"percent_complete,omitempty"`
}

// Audible-side status of an item.
const (
	arsAudibleFinished   = "finished"
	arsAudibleInProgress = "in_progress"
	arsAudibleNotStarted = "not_started"
)

// status classifies the item. is_finished decides first: a finished title
// usually reports percent_complete 99, not 100. The top-level flag is read
// first and listening_status's only when the top level has none; when both
// are present and disagree, finishedConflict reports it and the fixer makes
// the item a review row.
func (it arsItem) status() string {
	fin := false
	switch {
	case it.IsFinished != nil:
		fin = bool(*it.IsFinished)
	case it.Listening != nil && it.Listening.IsFinished != nil:
		fin = bool(*it.Listening.IsFinished)
	}
	if fin {
		return arsAudibleFinished
	}
	if it.percent() > 0 {
		return arsAudibleInProgress
	}
	return arsAudibleNotStarted
}

// finishedConflict: the export's top-level is_finished and
// listening_status.is_finished are both present and disagree. The real
// export has such titles (finished at the top level, not in
// listening_status, or the reverse), and nothing says which is right.
func (it arsItem) finishedConflict() bool {
	return it.IsFinished != nil && it.Listening != nil && it.Listening.IsFinished != nil &&
		bool(*it.IsFinished) != bool(*it.Listening.IsFinished)
}

func (it arsItem) percent() float64 {
	if p := float64(it.PercentComplete); p > 0 {
		return p
	}
	if it.Listening != nil && it.Listening.PercentComplete != nil {
		return float64(*it.Listening.PercentComplete)
	}
	return 0
}

// timestamp is Audible's listening time for the item. The export has one
// timestamp field for it, listening_status.finished_at_timestamp: the
// finish on a finished title, and (checked against a real export: present
// on about half the in-progress titles, always after the purchase date) the
// last listen on an in-progress one. present=false when the field is absent
// or empty. ok=false when it is present but not an RFC 3339 time WITH a zone
// ("Z" or an offset): a zone-less time is not read as UTC or as local time,
// and the fixer makes the item a review row. The fixer never substitutes
// "now".
func (it arsItem) timestamp() (t time.Time, present, ok bool) {
	if it.Listening == nil {
		return time.Time{}, false, false
	}
	v := strings.TrimSpace(it.Listening.FinishedAt)
	if v == "" {
		return time.Time{}, false, false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, true, false
	}
	return t.UTC(), true, true
}

// runtimeSeconds is Audible's runtime of its edition (0 = unknown).
func (it arsItem) runtimeSeconds() float64 { return float64(it.RuntimeMin) * 60 }

func (it arsItem) authorLine() string { return strings.Join(it.Authors, ", ") }

// arsNames reads a list of credits as the export tools write it: an array of
// {"name": ...} objects, an array of strings, or one comma-separated string.
// It is written back as an array of strings.
type arsNames []string

func (n *arsNames) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = nil
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*n = splitNames(s)
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("names: want a string or an array: %w", err)
	}
	out := make(arsNames, 0, len(raw))
	for _, r := range raw {
		var s string
		if json.Unmarshal(r, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
			continue
		}
		var o struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(r, &o); err != nil {
			return fmt.Errorf("names: entry %s is neither a string nor {name}", string(r))
		}
		if s = strings.TrimSpace(o.Name); s != "" {
			out = append(out, s)
		}
	}
	*n = out
	return nil
}

func splitNames(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// arsNum reads a number written as a JSON number or a numeric string; null
// and "" read as 0.
type arsNum float64

func (n *arsNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s == "" {
			*n = 0
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("number %q: %w", s, err)
		}
		*n = arsNum(f)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = arsNum(f)
	return nil
}

// arsBool reads a JSON boolean or "true"/"false" in any case; null is false.
type arsBool bool

func (v *arsBool) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*v = false
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*v = arsBool(strings.EqualFold(strings.TrimSpace(s), "true"))
		return nil
	}
	var x bool
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	*v = arsBool(x)
	return nil
}

// ---- title and author normalization ----

var (
	arsEditionRe = regexp.MustCompile(`(?i)[(\[]\s*(?:un)?abridged(?:\s+edition)?\s*[)\]]`)
	arsNumberRe  = regexp.MustCompile(`(?i)\b(?:book|vol|volume|part|no|number)\.?\s*#?\s*(\d+)\b`)
	arsDigitsRe  = regexp.MustCompile(`\d+`)
	arsArticleRe = regexp.MustCompile(`(?i)^\s*(?:the|a|an)\s+`)
)

// arsTitleKey is a title folded for matching: edition markers ("(Unabridged)")
// and a leading article dropped, "Book 03" read as "3", then case, accents,
// punctuation and spacing removed. main is the part before the first colon
// (the title without its subtitle). nums is the title's set of numbers
// (series positions), which two titles must share to match.
type arsTitleKey struct {
	full, main, nums string
}

func arsKeyOf(title string) arsTitleKey {
	t := arsEditionRe.ReplaceAllString(title, " ")
	t = arsNumberRe.ReplaceAllString(t, " $1 ")
	main := t
	if i := strings.Index(t, ":"); i > 0 {
		main = t[:i]
	}
	fold := func(s string) string {
		s = arsArticleRe.ReplaceAllString(s, "")
		s = arsDigitsRe.ReplaceAllStringFunc(s, func(d string) string {
			if n, err := strconv.Atoi(d); err == nil {
				return " " + strconv.Itoa(n) + " "
			}
			return d
		})
		return authorjunk.FoldKey(s)
	}
	var nums []int
	seen := map[int]bool{}
	for _, d := range arsDigitsRe.FindAllString(t, -1) {
		if n, err := strconv.Atoi(d); err == nil && !seen[n] {
			seen[n] = true
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return arsTitleKey{full: fold(t), main: fold(main), nums: strings.Join(parts, ",")}
}

// arsTitlesMatch: the same title, or the same title with a subtitle dropped
// on one side only ("Title: Sub" vs "Title"), with the same numbers. Two
// different subtitles never match ("Title: One" vs "Title: Two").
func arsTitlesMatch(a, b arsTitleKey) bool {
	if a.full == "" || b.full == "" || a.nums != b.nums {
		return false
	}
	return a.full == b.full || a.full == b.main || a.main == b.full
}

// arsSurnames are the folded last words of each name, the part two spellings
// of one author ("J. Smith", "John Smith") share.
func arsSurnames(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		f := strings.Fields(strings.NewReplacer(".", " ", ",", " ").Replace(n))
		if len(f) == 0 {
			continue
		}
		if k := authorjunk.FoldKey(f[len(f)-1]); len(k) >= 2 {
			out[k] = true
		}
	}
	return out
}

func arsAuthorsOverlap(a, b []string) bool {
	sa := arsSurnames(a)
	for k := range arsSurnames(b) {
		if sa[k] {
			return true
		}
	}
	return false
}

// arsRuntimeTolerance is how far a local copy's duration may be from
// Audible's runtime and still be the same edition: ±10%.
const arsRuntimeTolerance = 0.10

// arsRuntime compares a local duration with Audible's runtime (both seconds).
// known=false when either is unknown (0).
func arsRuntime(localSec, audibleSec float64) (ok, known bool) {
	if localSec <= 0 || audibleSec <= 0 {
		return true, false
	}
	return math.Abs(localSec-audibleSec) <= arsRuntimeTolerance*audibleSec, true
}

// ---- library index ----

// arsLibrary is the plan's whole-library index: every live book by ASIN, by
// title key and by primary author's folded name, and the primaries of each
// version group. Built once per plan, read-only afterwards (the per-item
// workers share it without a lock).
type arsLibrary struct {
	books     map[string]*database.BookCore
	byASIN    map[string][]string
	byTitle   map[string][]string
	byAuthor  map[string][]string
	primaries map[string][]string
	// members is every live book of each version group, primary or not.
	members map[string][]string
	authors map[int]string
	keys    map[string]arsTitleKey
}

func buildARSLibrary(cores []database.BookCore, authors []database.Author) *arsLibrary {
	lib := &arsLibrary{books: map[string]*database.BookCore{}, byASIN: map[string][]string{},
		byTitle: map[string][]string{}, byAuthor: map[string][]string{}, primaries: map[string][]string{},
		authors: map[int]string{}, keys: map[string]arsTitleKey{}, members: map[string][]string{}}
	for i := range authors {
		lib.authors[authors[i].ID] = authors[i].Name
	}
	for i := range cores {
		b := &cores[i]
		if b.IsSoftDeleted() {
			continue
		}
		lib.books[b.ID] = b
		if b.ASIN != nil {
			if a := strings.ToUpper(strings.TrimSpace(*b.ASIN)); a != "" {
				lib.byASIN[a] = append(lib.byASIN[a], b.ID)
			}
		}
		k := arsKeyOf(b.Title)
		lib.keys[b.ID] = k
		if k.full != "" {
			lib.byTitle[k.full] = append(lib.byTitle[k.full], b.ID)
			if k.main != k.full && k.main != "" {
				lib.byTitle[k.main] = append(lib.byTitle[k.main], b.ID)
			}
		}
		if b.AuthorID != nil {
			if n := arsKeyOf(lib.authors[*b.AuthorID]).full; n != "" {
				lib.byAuthor[n] = append(lib.byAuthor[n], b.ID)
			}
		}
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			lib.members[*b.VersionGroupID] = append(lib.members[*b.VersionGroupID], b.ID)
		}
		if b.VersionGroupID != nil && *b.VersionGroupID != "" && b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
			lib.primaries[*b.VersionGroupID] = append(lib.primaries[*b.VersionGroupID], b.ID)
		}
	}
	return lib
}

// targets resolves hits to the books whose state the import would write:
// each hit's version-group primary, or the hit itself when it has no group or
// its group has no primary. Sorted, without duplicates.
func (lib *arsLibrary) targets(hits []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range hits {
		b := lib.books[id]
		if b == nil {
			continue
		}
		if b.VersionGroupID != nil && len(lib.primaries[*b.VersionGroupID]) > 0 {
			for _, p := range lib.primaries[*b.VersionGroupID] {
				add(p)
			}
			continue
		}
		add(id)
	}
	sort.Strings(out)
	return out
}

// titleHits are the live books whose title matches the item's (arsTitlesMatch).
func (lib *arsLibrary) titleHits(k arsTitleKey) []string {
	seen := map[string]bool{}
	var out []string
	for _, key := range []string{k.full, k.main} {
		for _, id := range lib.byTitle[key] {
			if !seen[id] && arsTitlesMatch(k, lib.keys[id]) {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}
