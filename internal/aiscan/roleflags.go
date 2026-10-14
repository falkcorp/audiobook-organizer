// file: internal/aiscan/roleflags.go
// version: 1.1.0
// guid: 91c4e7b2-6d38-4a05-b8f1-3e2a7d5c9046
// last-edited: 2026-10-14

package aiscan

import (
	"encoding/json"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// RawRoleFlags are the "this one is a narrator / publisher" answers the model
// gave, read back from the raw output a scan phase stored (SavePhaseData keeps the
// model's answers as returned, before they are converted to suggestions).
//
// Scans stored before the converters kept the deprecated is_narrator /
// is_publisher lists have no flag on their results at all, and the batch prompts
// ask for nothing else, so the stored phase output is the only place those answers
// still exist. The apply consults it to refuse a merge that would fold a flagged
// author into the keep author (Unaccounted).
type RawRoleFlags struct {
	// Authors are author ids a full-scan answer flagged as narrator or publisher.
	Authors map[int]bool
	// Groups are the group indexes a groups-scan answer flagged something in. The
	// flag names positions in the group, so the apply cannot tell WHICH author.
	Groups map[int]bool
}

// LoadRawRoleFlags reads the flags out of a scan's stored phase output. A phase
// with no output (an empty source, a phase that never ran) contributes nothing; an
// output that cannot be decoded is an error, so a caller fails closed instead of
// treating an unreadable answer as "nothing flagged".
func LoadRawRoleFlags(store *database.AIScanStore, scanID int) (RawRoleFlags, error) {
	flags := RawRoleFlags{Authors: map[int]bool{}, Groups: map[int]bool{}}
	read := func(phase string) (json.RawMessage, error) {
		p, err := store.GetPhase(scanID, phase)
		if err != nil {
			return nil, fmt.Errorf("read phase %s: %w", phase, err)
		}
		if p == nil || len(p.OutputData) == 0 || string(p.OutputData) == "null" {
			return nil, nil
		}
		return p.OutputData, nil
	}
	// Full answers (a full scan, and either enrichment, which re-asks the full
	// question): ids are author ids.
	for _, phase := range []string{"full_scan", "full_enrich", "groups_enrich"} {
		raw, err := read(phase)
		if err != nil {
			return flags, err
		}
		if raw == nil {
			continue
		}
		var out []ai.AuthorDiscoverySuggestion
		if err := json.Unmarshal(raw, &out); err != nil {
			return flags, fmt.Errorf("decode %s output of scan %d: %w", phase, scanID, err)
		}
		for _, s := range out {
			for _, id := range s.IsNarrator {
				flags.Authors[id] = true
			}
			for _, id := range s.IsPublisher {
				flags.Authors[id] = true
			}
			if s.Roles != nil {
				for _, r := range []*ai.SuggestionRole{s.Roles.Narrator, s.Roles.Publisher} {
					if r != nil {
						for _, id := range r.IDs {
							flags.Authors[id] = true
						}
					}
				}
			}
		}
	}
	raw, err := read("groups_scan")
	if err != nil {
		return flags, err
	}
	if raw != nil {
		var out []ai.AuthorDedupSuggestion
		if err := json.Unmarshal(raw, &out); err != nil {
			return flags, fmt.Errorf("decode groups_scan output of scan %d: %w", scanID, err)
		}
		for _, s := range out {
			narr := len(s.IsNarrator) > 0 || len(s.IsPublisher) > 0
			if s.Roles != nil {
				narr = narr || (s.Roles.Narrator != nil && len(s.Roles.Narrator.IDs) > 0) ||
					(s.Roles.Publisher != nil && len(s.Roles.Publisher.IDs) > 0)
			}
			if narr {
				flags.Groups[s.GroupIndex] = true
			}
		}
	}
	return flags, nil
}

// Unaccounted reports why a merge row must be refused because the stored phase
// output flags an author of it that the row's own Roles do not carry, or "" when
// nothing is unaccounted for. carried is the set of ids the row's Roles name as
// narrator or publisher (the apply handles those itself); source and groupIndex are
// the row's Source and GroupIndex.
func (f RawRoleFlags) Unaccounted(source string, groupIndex int, authorIDs []int, carried map[int]bool) string {
	if source == "full_scan" || source == "cross_validate" {
		for _, id := range authorIDs {
			if f.Authors[id] && !carried[id] {
				return fmt.Sprintf("the scan's stored answer flags author %d as a narrator or publisher, but this result does not carry the flag", id)
			}
		}
	}
	// A positional flag is refused whatever the row carries: a row whose Roles
	// came from the full scan names author ids, which say nothing about WHICH
	// author the groups answer meant, and a row carrying the positions is refused
	// by the apply anyway.
	if (source == "groups_scan" || source == "cross_validate") && f.Groups[groupIndex] {
		return "the scan's stored answer flags a narrator or publisher in this group by position, so which author is meant is unknown"
	}
	return ""
}

// RolesNameAuthorIDs reports whether the ids in a suggestion's Roles are author
// ids. A full-scan answer names authors by id; a groups-scan answer names them
// by position in its group, which the apply cannot map to an author. A
// cross_validate row carries one or the other (RolesSource); one stored before
// RolesSource existed cannot tell and is read as positions, so a narrator flag
// on it is refused rather than guessed at.
func RolesNameAuthorIDs(s database.ScanSuggestion) bool {
	switch s.Source {
	case "full_scan":
		return true
	case "cross_validate":
		return s.RolesSource == "full_scan"
	}
	return false
}
