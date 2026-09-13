// file: internal/metadata/contributor_roles.go
// version: 1.0.0
// guid: 150f76bd-87b2-4ef6-bec0-5d1593f3becc
// last-edited: 2026-09-13

package metadata

import (
	"regexp"
	"strings"
)

// ContributorRole classifies a credited name for the author string.
type ContributorRole int

const (
	// RoleAuthor is a writer of the work: it belongs in the author string.
	RoleAuthor ContributorRole = iota
	// RoleNarrator reads the audiobook: it belongs in the narrator field.
	RoleNarrator
	// RoleOther is an editor, translator, illustrator, foreword writer, etc.
	// It belongs in neither the author string nor the narrator field.
	RoleOther
)

// roleSuffix matches a role written after the name, the forms providers use:
// "John Joseph Adams - editor", "Jane Doe (Translator)", "Jane Doe [ed.]",
// "Jane Doe, illustrator".
var roleSuffix = regexp.MustCompile(`(?i)^(.*?)\s*(?:[-–—,]\s*|\(\s*|\[\s*)(editor|compiler|translator|trans\.|illustrator|narrator|reader|foreword|introduction|afterword|contributor|adapter)\s*[)\]]?\s*$`)

// roleEdAbbrev matches the "ed." abbreviation only where it cannot be a given
// name: in brackets ("Jane Doe (ed.)", "Jane Doe [eds]") or after a comma WITH
// the period ("Jane Doe, ed."). A bare "Doe, Ed" is a person called Ed.
var roleEdAbbrev = regexp.MustCompile(`(?i)^(.*?)\s*(?:\(\s*eds?\.?\s*\)|\[\s*eds?\.?\s*\]|,\s*eds?\.)\s*$`)

// rolePrefix matches a role written before the name: "edited by Jane Doe",
// "Translated by Jane Doe", "Read by Jane Doe".
var rolePrefix = regexp.MustCompile(`(?i)^\s*(edited|compiled|translated|illustrated|narrated|read|adapted|introduced)\s+by\s+(.+?)\s*$`)

// roleFromWord maps a role word to its classification.
func roleFromWord(w string) ContributorRole {
	w = strings.ToLower(strings.TrimSpace(w))
	switch {
	case strings.HasPrefix(w, "narrat"), strings.HasPrefix(w, "read"):
		return RoleNarrator
	default:
		return RoleOther
	}
}

// ClassifyContributor splits a credited name into the bare name and its role.
// A name with no recognisable role marker is RoleAuthor, unchanged apart from
// trimming. On 2026-09-13 a bulk apply created an author literally named
// "John Joseph Adams - editor"; every provider credit that can carry a role in
// its text goes through here before it reaches the author string.
func ClassifyContributor(credit string) (string, ContributorRole) {
	credit = strings.TrimSpace(credit)
	if m := rolePrefix.FindStringSubmatch(credit); m != nil {
		return strings.TrimSpace(m[2]), roleFromWord(m[1])
	}
	if m := roleSuffix.FindStringSubmatch(credit); m != nil && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1]), roleFromWord(m[2])
	}
	if m := roleEdAbbrev.FindStringSubmatch(credit); m != nil && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1]), RoleOther
	}
	return credit, RoleAuthor
}

// ClassifyContributorRoleName maps a structured role name (Open Library edition
// contributors[].role: "Editor", "Translator", "Narrator", "Illustrator"...) to
// a classification. An empty role or an authorship role ("Author", "Writer")
// is RoleAuthor.
func ClassifyContributorRoleName(role string) ContributorRole {
	r := strings.ToLower(strings.TrimSpace(role))
	switch {
	case r == "", strings.HasPrefix(r, "author"), strings.HasPrefix(r, "writer"), r == "co-author", r == "coauthor":
		return RoleAuthor
	case strings.HasPrefix(r, "narrat"), strings.HasPrefix(r, "read"), strings.Contains(r, "reader"), strings.HasPrefix(r, "performer"):
		return RoleNarrator
	default:
		return RoleOther
	}
}

// PartitionCredits classifies each credit and returns the bare author names
// and narrator names in input order, de-duplicated case-insensitively. Other
// roles are dropped.
func PartitionCredits(credits []string) (authors, narrators []string) {
	seenA := map[string]bool{}
	seenN := map[string]bool{}
	for _, c := range credits {
		name, role := ClassifyContributor(c)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		switch role {
		case RoleAuthor:
			if !seenA[key] {
				seenA[key] = true
				authors = append(authors, name)
			}
		case RoleNarrator:
			if !seenN[key] {
				seenN[key] = true
				narrators = append(narrators, name)
			}
		}
	}
	return authors, narrators
}
