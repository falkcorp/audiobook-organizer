// file: internal/decisions/record.go
// version: 1.0.0
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
// Owner rules this store encodes (2026-10-20):
//   - A reject blocks only its own proposal kind. The verdict is keyed by
//     op_kind, and a pair edge carries an op family (FamilySameWork, ...)
//     that PairVetoed matches exactly. FamilyAny, which only the "never
//     merge" override (a later PR) uses, matches every family.
//   - Approve, applied, auto_applied, undone and skip are labels. None of
//     them blocks anything. An undo does not veto: the owner may have undone
//     it for an unrelated reason. Skip is a weak signal.
//   - There is no approve-but-do-not-apply decision.
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
	// proposal (field-rewrite ops only; see MaterialHash). It lifts the veto.
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
	// FamilyAny, as an edge's family, vetoes the pair for every family. Only
	// the owner's "never merge" override uses it, and that write path is a
	// later PR: Record refuses it, so an ordinary reject can never block every
	// kind. The lookups already honour it.
	FamilyAny = "*"
)

// Families are the op families a reject may carry.
var Families = []string{FamilySameWork, FamilySameCopy, FamilySamePerson, FamilySameSeries}

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
	// StateReopened: the data changed materially after the reject. Blocks
	// nothing, and is never auto-applied.
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
	// State is active.
	RejectID string `json:"reject_id,omitempty"`
	// MaterialHash is the rejected proposal's material hash.
	MaterialHash string `json:"material_hash,omitempty"`
}

// Blocks reports whether the verdict suppresses the proposal.
func (v *Verdict) Blocks() bool { return v != nil && v.State == StateActive }

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
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	ids := sortedUnique(in.EntityIDs)
	if len(ids) == 0 {
		return nil, invalid("entity_ids is empty")
	}
	for _, id := range ids {
		if err := checkComponent("entity_id", id); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
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
