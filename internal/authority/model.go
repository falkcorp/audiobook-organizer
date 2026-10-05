// file: internal/authority/model.go
// version: 1.0.0
// guid: 2e7a9c14-6b3f-4d58-8a1e-f04c9d2b7e61
// last-edited: 2026-10-04

package authority

import "time"

// RoleStat is the evidence for one role: observations per tier, their sum,
// and the best tier seen.
type RoleStat struct {
	Count  int          `json:"count"`
	Tier   Tier         `json:"tier"`
	ByTier map[Tier]int `json:"by_tier"`
}

// Person is a ref_person: row.
type Person struct {
	Fold     string   `json:"fold"`
	Display  string   `json:"display"`
	Variants []string `json:"variants,omitempty"`
	ASINs    []string `json:"asins,omitempty"`
	// HomonymASINs is set when the fold carries more than one contributor
	// ASIN: two people may share the spelling. Reported, never resolved.
	HomonymASINs  bool              `json:"homonym_asins,omitempty"`
	Roles         map[Role]RoleStat `json:"roles"`
	Sources       []string          `json:"sources"`
	Tier          Tier              `json:"tier"`
	ProductSample []string          `json:"product_sample,omitempty"`
	FirstSeen     time.Time         `json:"first_seen"`
	LastSeen      time.Time         `json:"last_seen"`
}

// Publisher is a ref_pub: row.
type Publisher struct {
	Fold     string   `json:"fold"`
	Display  string   `json:"display"`
	Variants []string `json:"variants,omitempty"`
	// Count is the number of products seen with this publisher;
	// ManualOnlyCount how many of them were manual-only (Doctor Who / Big
	// Finish / Torchwood). The seed contributes no products.
	Count           int       `json:"count"`
	ManualOnlyCount int       `json:"manual_only_count"`
	Sources         []string  `json:"sources"`
	Tier            Tier      `json:"tier"`
	ProductSample   []string  `json:"product_sample,omitempty"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

// ASINRef is a ref_asin:person: row: every fold a contributor ASIN was
// credited under. More than one fold is a spelling variant ("Jonathan Brazee"
// / "Jonathan P. Brazee"); all are kept, none is picked.
type ASINRef struct {
	ASIN  string   `json:"asin"`
	Folds []string `json:"folds"`
}

// AuthorEvidenceRule reports whether a person's author role is author
// evidence (design: roles.author >= 1 at tier O or A, or >= 2 counting tier
// B). Tier C alone never is, and cast_author never counts.
func AuthorEvidenceRule(st RoleStat) bool {
	strong := st.ByTier[TierO] + st.ByTier[TierA]
	return strong >= 1 || strong+st.ByTier[TierB] >= 2
}

// NarratorEvidenceRule: any structured observation (O, A or B), or two of
// any tier.
func NarratorEvidenceRule(st RoleStat) bool {
	return st.ByTier[TierO]+st.ByTier[TierA]+st.ByTier[TierB] >= 1 || st.Count >= 2
}
