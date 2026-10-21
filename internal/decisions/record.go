// file: internal/decisions/record.go
// version: 1.1.0
// guid: 91d4e7b2-6a03-4c58-b1f9-0e5d28a7c463
// last-edited: 2026-10-21

// Package decisions is the durable store of owner decisions about proposals:
// every reject, approve, apply and undo is kept as a labelled example (an
// evidence snapshot, a reason, who decided and when), and a reject also
// suppresses the same proposal from being found again until it is
// un-rejected.
//
// This package only SUPPRESSES and ANNOTATES. It writes decision: keys and
// nothing else: no book, author, series or book_file row. Decisions are
// durable and are never pruned.
//
// Owner rules this store encodes (2026-10-20, refined 2026-10-21):
//   - A reject blocks only its own QUESTION FAMILY. The verdict is keyed by
//     op_kind (that exact proposal), and a pair edge carries an op family
//     (FamilySameWork, ...) that PairVetoed matches exactly. So a reject made
//     in repairs:dedup.split-books, family same_work, also blocks
//     repairs:fragment-consolidation and repairs:folder-books from proposing
//     the same pair (they ask the same question), and does NOT block a
//     same_copy (duplicate-copies, version-twin) proposal of that pair.
//     FamilyAny, which only the "never merge" override (a later PR) uses,
//     matches every family.
//   - A reopen allows NEW suggestions only. When the data behind a
//     field-rewrite proposal changes, a reopened record lets the changed
//     proposal be made again, but the originally rejected value stays blocked
//     for good: if the data flips back to it, it is rejected again. The
//     verdict keeps every rejected material hash for that. Only an unreject
//     (a human, explicitly) lifts a block.
//   - Approve, applied, auto_applied, undone and skip are labels. None of
//     them blocks anything and none lifts a block. "approve" records that the
//     owner accepted the proposal; carrying it out is a separate applied or
//     auto_applied record, and there is no approve-but-do-not-apply decision.
//     An undo does not veto: the owner may have undone it for an unrelated
//     reason. Skip is a weak signal.
//   - Only a human replaces or lifts a human's reject.
package decisions

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// SchemaVersion is stamped on every Record so an exported line is
// self-describing.
const SchemaVersion = "1"

// Limits enforced by validation (an over-limit input is refused, never
// silently truncated).
const (
	MaxReasonTextLen = 2000
	// MaxEdgeSet is the largest rejected set stored as pairwise edges
	// (n*(n-1)/2 rows). A larger set is stored as one membership row per
	// member (n rows) and vetoes exactly the same pairs.
	//
	// FROZEN: never edit this in place. Each reject record persists the shape
	// it was written in (Record.IndexShape) and its undo deletes by that shape,
	// so a changed value would not strand old rows, but readers and the docs
	// quote 50 and a retuned limit belongs in a new, deliberate PR with a
	// migration plan, not a drive-by edit.
	MaxEdgeSet = 50
	// MaxSnapshotPaths caps the file paths kept per entity snapshot.
	MaxSnapshotPaths = 50
)

// Decision is what a person (or an auto path) did with a proposal.
type Decision string

const (
	DecisionReject      Decision = "reject"
	DecisionApprove     Decision = "approve"
	DecisionApplied     Decision = "applied"
	DecisionUndone      Decision = "undone"
	DecisionUnreject    Decision = "unreject"
	DecisionAutoApplied Decision = "auto_applied"
	// DecisionReopened records that changed data re-opened a rejected
	// proposal so NEW suggestions may be made (field-rewrite ops only: the
	// reject and the reopen both carry a MaterialHash and the two differ). It
	// does NOT lift the veto on the rejected value: that stays blocked, and so
	// do its pair edges. Only an unreject lifts it.
	DecisionReopened Decision = "reopened"
	// DecisionSkip is the weak "skip for now" signal. It never vetoes.
	DecisionSkip Decision = "skip"
)

var validDecisions = []Decision{
	DecisionReject, DecisionApprove, DecisionApplied, DecisionUndone,
	DecisionUnreject, DecisionAutoApplied, DecisionReopened, DecisionSkip,
}

// Source says who made the decision.
type Source string

const (
	SourceHuman Source = "human"
	SourceAuto  Source = "auto"
	SourceLLM   Source = "llm"
)

// Op families scope a pair veto: rejecting "these two are not split parts of
// one work" must not assert "never version siblings".
const (
	FamilySameWork   = "same_work"   // split / fragment / folder-books / consolidation
	FamilySameCopy   = "same_copy"   // dedup / duplicate-copies / version-twin
	FamilySamePerson = "same_person" // authors
	FamilySameSeries = "same_series"
	// FamilyAny has two meanings, by direction.
	//
	// STORED on an edge, it vetoes the pair for every family. Only the owner's
	// "never merge" override uses it, and that write path is a later PR:
	// Record refuses it, so an ordinary reject can never block every kind. The
	// lookups already honour it.
	//
	// QUERIED (PairVetoed's family argument), it asks "is this pair vetoed in
	// ANY family?", for a screen that shows whether a pair carries a reject at
	// all. A producer deciding whether to propose a pair must ask its own
	// family, never FamilyAny.
	FamilyAny = "*"
)

// Families are the op families a reject may carry, and the only families (plus
// FamilyAny) a PairVetoed query accepts: an unknown one is an error, never
// "not vetoed" (a typo would otherwise silently fail open).
var Families = []string{FamilySameWork, FamilySameCopy, FamilySamePerson, FamilySameSeries}

// Index shapes: how a reject of two or more entities stored its veto rows.
const (
	// IndexEdges: one decision:pe: row per pair.
	IndexEdges = "edges"
	// IndexMembers: one decision:mb: row per member.
	IndexMembers = "members"
)

// indexShapeFor is the shape a reject of n entities is written in.
func indexShapeFor(n int) string {
	switch {
	case n < 2:
		return ""
	case n <= MaxEdgeSet:
		return IndexEdges
	default:
		return IndexMembers
	}
}

// ErrInvalid wraps every validation failure, so a handler can answer 400 for
// it and 500 for anything else.
var ErrInvalid = errors.New("decisions: invalid input")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// EntitySnapshot is the state of one entity at decision time.
type EntitySnapshot struct {
	EntityType string            `json:"entity_type"`
	EntityID   string            `json:"entity_id"`
	Label      string            `json:"label,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	// Paths are the entity's file paths, at most MaxSnapshotPaths.
	Paths []string `json:"paths,omitempty"`
}

// Snapshot is the evidence a decision was made on. The server builds it from
// the stored plan row, candidate or hold BEFORE the write; a client never
// sends one.
type Snapshot struct {
	Entities []EntitySnapshot  `json:"entities,omitempty"`
	Current  map[string]string `json:"current,omitempty"`
	Proposed map[string]string `json:"proposed,omitempty"`
	Evidence []string          `json:"evidence,omitempty"`
	Score    float64           `json:"score,omitempty"`
	Band     string            `json:"band,omitempty"`
	Class    string            `json:"class,omitempty"`
}

// Input is what a caller supplies to Store.Record. The store fills the id,
// schema version, subject key, timestamp and supersedes link.
type Input struct {
	// OpKind names the proposal kind, e.g. "repairs:dedup.split-books",
	// "dedup:book-candidate", "authors:group-merge", "metadata:candidate".
	OpKind                 string
	OpVersion, RuleVersion string

	EntityType string
	EntityIDs  []string
	// Scope separates different proposals about the same entities; see
	// SubjectKey.
	Scope string
	// MaterialHash is MaterialHash(Snapshot.Proposed) for a field-rewrite op,
	// "" for a sticky one.
	MaterialHash string

	// PlanOpID and PlanRowID are provenance: the repairs plan op and row, the
	// dedup candidate id, the review item id, the AI-scan result id.
	PlanOpID, PlanRowID string

	Snapshot Snapshot
	Decision Decision
	Source   Source
	// Family scopes the pair veto of a reject of two or more entities.
	// Required for such a reject; ignored for every other decision.
	Family string

	ReasonCode, ReasonText string
	ActorUserID, ActorName string
}

// Record is one immutable row of the decision log, and one labelled example.
type Record struct {
	ID            string `json:"id"`
	SchemaVersion string `json:"schema_version"`

	OpKind       string   `json:"op_kind"`
	OpVersion    string   `json:"op_version,omitempty"`
	RuleVersion  string   `json:"rule_version,omitempty"`
	SubjectKey   string   `json:"subject_key"`
	Scope        string   `json:"scope,omitempty"`
	MaterialHash string   `json:"material_hash,omitempty"`
	EntityType   string   `json:"entity_type"`
	EntityIDs    []string `json:"entity_ids"`
	PlanOpID     string   `json:"plan_op_id,omitempty"`
	PlanRowID    string   `json:"plan_row_id,omitempty"`

	Snapshot Snapshot `json:"snapshot"`
	Decision Decision `json:"decision"`
	Source   Source   `json:"source"`
	Family   string   `json:"family,omitempty"`
	// IndexShape is how a reject of two or more entities stored its veto rows
	// (IndexEdges or IndexMembers). It is persisted so the undo deletes exactly
	// the rows that were written, whatever MaxEdgeSet is by then.
	IndexShape string `json:"index_shape,omitempty"`

	ReasonCode  string    `json:"reason_code,omitempty"`
	ReasonText  string    `json:"reason_text,omitempty"`
	ActorUserID string    `json:"actor_user_id,omitempty"`
	ActorName   string    `json:"actor_name,omitempty"`
	DecidedAt   time.Time `json:"decided_at"`
	// SupersedesID is the previous record on the same subject, so a
	// subject's history is one linear chain.
	SupersedesID string `json:"supersedes_id,omitempty"`
}

// VerdictState is the current state of a proposal's rejection.
type VerdictState string

const (
	// StateNone: decided about (applied, approved, skipped...) but never
	// rejected. Blocks nothing.
	StateNone VerdictState = "none"
	// StateActive: rejected. The proposal, and its pairs, are vetoed.
	StateActive VerdictState = "active"
	// StateUnrejected: the owner lifted the reject. Blocks nothing.
	StateUnrejected VerdictState = "unrejected"
	// StateReopened: the data changed materially after the reject, so new
	// suggestions are allowed; never auto-applied. The rejected value is STILL
	// blocked (RejectedHashes), and so are the reject's pair edges.
	StateReopened VerdictState = "reopened"
)

// Verdict is the current state of one proposal, keyed by (OpKind, SubjectKey).
type Verdict struct {
	OpKind     string       `json:"op_kind"`
	SubjectKey string       `json:"subject_key"`
	State      VerdictState `json:"state"`
	// LatestID is the newest record of any decision on the subject.
	LatestID string `json:"latest_id"`
	// RejectID is the reject record whose pair edges are live; "" unless
	// State is active or reopened.
	RejectID string `json:"reject_id,omitempty"`
	// RejectedHashes are the material hashes still blocked, sorted. Empty
	// while the verdict is live means a sticky reject: it blocks the proposal
	// whatever its data. A reject with a hash adds to the set, a reopen never
	// removes from it, an unreject clears it.
	RejectedHashes []string `json:"rejected_hashes,omitempty"`
}

// Live reports whether a reject is in force: active, or reopened (a reopen
// leaves the rejected value blocked).
func (v *Verdict) Live() bool {
	return v != nil && (v.State == StateActive || v.State == StateReopened)
}

// BlocksMaterial reports whether the verdict suppresses a proposal whose
// current material hash is materialHash ("" for a sticky op, whose data
// cannot be told apart). It blocks when a reject is live and either the
// reject is sticky, the caller gave no hash, or the hash is one that was
// rejected. A hash that was never rejected is a new suggestion.
func (v *Verdict) BlocksMaterial(materialHash string) bool {
	return v.Live() && blocksMaterial(v.RejectedHashes, materialHash)
}

func blocksMaterial(rejected []string, materialHash string) bool {
	return len(rejected) == 0 || materialHash == "" || slices.Contains(rejected, materialHash)
}

// ActiveRow is the value of a decision:va: row: what Load needs to apply a
// live reject without reading its verdict.
type ActiveRow struct {
	RejectedHashes []string `json:"rejected_hashes,omitempty"`
}

func (d Decision) valid() bool { return slices.Contains(validDecisions, d) }

func (s Source) valid() bool {
	return s == SourceHuman || s == SourceAuto || s == SourceLLM
}

// validate checks in and returns the sorted, de-duplicated entity ids.
func (in *Input) validate() ([]string, error) {
	for _, c := range []struct{ field, v string }{
		{"op_kind", in.OpKind}, {"entity_type", in.EntityType},
	} {
		if err := checkComponent(c.field, c.v); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	ids, err := canonicalIDs(in.EntityIDs)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, invalid("entity_ids is empty")
	}
	if !in.Decision.valid() {
		return nil, invalid("unknown decision %q", in.Decision)
	}
	if !in.Source.valid() {
		return nil, invalid("source must be human, auto or llm, got %q", in.Source)
	}
	if in.Source == SourceHuman && strings.TrimSpace(in.ActorUserID) == "" {
		return nil, invalid("a human decision must carry actor_user_id")
	}
	if in.Decision == DecisionUnreject && in.Source != SourceHuman {
		return nil, invalid("only a human can unreject, got source %q", in.Source)
	}
	if in.Decision == DecisionReopened && in.MaterialHash == "" {
		return nil, invalid("a reopen must carry the new material_hash")
	}
	if len(in.ReasonText) > MaxReasonTextLen {
		return nil, invalid("reason_text is longer than %d characters", MaxReasonTextLen)
	}
	if in.Decision == DecisionReject && in.ReasonCode == "" {
		return nil, invalid("a reject must carry a reason_code (use %q when none fits)", ReasonOther)
	}
	if in.ReasonCode != "" && !ReasonKnown(in.OpKind, in.ReasonCode) {
		return nil, invalid("reason_code %q is not in the taxonomy for %q", in.ReasonCode, in.OpKind)
	}
	if in.Decision == DecisionReject && len(ids) >= 2 {
		if !slices.Contains(Families, in.Family) {
			return nil, invalid("a reject of %d entities needs a family, one of %v, got %q", len(ids), Families, in.Family)
		}
	}
	for i := range in.Snapshot.Entities {
		if n := len(in.Snapshot.Entities[i].Paths); n > MaxSnapshotPaths {
			return nil, invalid("snapshot entity %d holds %d paths, at most %d", i, n, MaxSnapshotPaths)
		}
	}
	return ids, nil
}
