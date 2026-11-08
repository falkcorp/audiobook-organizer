// file: core/storage/claims.go
// version: 1.0.0
// guid: 8c26edbe-b7d5-48b8-91d5-7abe4ad6f7de
// last-edited: 2026-11-07

package storage

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/mediacore/storage/keys"
)

// Claim reserves legacy keys (written before core existed) for one owner.
// Exactly one of Prefix or Key is set. A claim's owner and mode are a function
// of the build alone; the only mode changes of ABO's claims are R2's, which
// retires the families the cutover drains, and R3's, which retires the
// settings blob, R2's one cutover key that stays read-write because M2
// rewrites it and M6 deletes it (§5S.9.1 row 7).
type Claim struct {
	Prefix      string    // e.g. "item:"; ends in a separator byte (':' '/' '_' '.'), else validation fails
	Key         string    // exact single key, e.g. "item_counter"
	Except      []string  // CROSS-OWNER carve-outs only: sub-prefixes or exact keys claimed by exactly one other owner
	Mode        ClaimMode // zero rejected
	Serialized  bool      // its writes take commit stripes; Expect allowed (§5S.3.2); a blind write is never refused in a claim (A-3)
	TakesOver   string    // optional "<former-owner>": explicit takeover of an orphaned claim (rule 5)
	Description string
}

// ClaimMode is what a claim's owner may do in it.
type ClaimMode uint8

const (
	ClaimInvalid   ClaimMode = iota
	ClaimReadWrite           // owner reads and writes in place
	ClaimReadOnly            // frozen: reads only; every write refused
	ClaimAgingOut            // reads + Delete/DeleteRange (retention, read-through cleanup); Set refused (DATA-15)
	ClaimRetired             // like AgingOut, and expected to drain to zero: census and health report a non-empty retired range
)

// String is the mode's name, as the claim ledger records it.
func (m ClaimMode) String() string {
	switch m {
	case ClaimReadWrite:
		return "read-write"
	case ClaimReadOnly:
		return "read-only"
	case ClaimAgingOut:
		return "aging-out"
	case ClaimRetired:
		return "retired"
	default:
		return fmt.Sprintf("invalid(%d)", uint8(m))
	}
}

// allows reports whether the mode admits op.
func (m ClaimMode) allows(op opKind) bool {
	switch op {
	case opRead:
		return m != ClaimInvalid
	case opSet:
		return m == ClaimReadWrite
	case opDelete:
		return m == ClaimReadWrite || m == ClaimAgingOut || m == ClaimRetired
	}
	return false
}

// opKind is what a claim check is asked to admit.
type opKind uint8

const (
	opRead opKind = iota
	opSet
	opDelete
)

func (op opKind) String() string {
	switch op {
	case opSet:
		return "set"
	case opDelete:
		return "delete"
	default:
		return "read"
	}
}

// ErrClaimConflict: the kernel's claim validation at boot found one or more
// conflicts (05-storage §5S.3.4); the error lists every conflict, not only
// the first.
var ErrClaimConflict = errors.New("storage: claim conflict")

// ClaimConflictError lists every claim conflict validation found.
type ClaimConflictError struct{ Problems []string }

func (e *ClaimConflictError) Error() string {
	return fmt.Sprintf("storage: %d claim conflict(s): %s", len(e.Problems), strings.Join(e.Problems, "; "))
}

func (e *ClaimConflictError) Is(target error) bool { return target == ErrClaimConflict }

// span is an absolute half-open key range [start, end).
type span struct{ start, end []byte }

func (s span) contains(k []byte) bool {
	return bytes.Compare(k, s.start) >= 0 && bytes.Compare(k, s.end) < 0
}

func (s span) overlaps(start, end []byte) bool {
	return bytes.Compare(start, s.end) < 0 && bytes.Compare(s.start, end) < 0
}

// claimRef is one declared claim with its owner and absolute span.
type claimRef struct {
	owner Owner
	c     Claim
	sp    span
	depth int // len of the prefix, or len(key)+1 for an exact key (always the longest match)
}

func (r claimRef) name() string {
	if r.c.Key != "" {
		return fmt.Sprintf("%s key %q", r.owner, r.c.Key)
	}
	return fmt.Sprintf("%s prefix %q", r.owner, r.c.Prefix)
}

func (r claimRef) target() string {
	if r.c.Key != "" {
		return r.c.Key
	}
	return r.c.Prefix
}

// claimSpan is the span a claim covers: [prefix, PrefixEnd(prefix)) or
// [key, key+0x00).
func claimSpan(c Claim) span {
	if c.Key != "" {
		k := []byte(c.Key)
		return span{start: k, end: append(slices.Clone(k), 0)}
	}
	p := []byte(c.Prefix)
	return span{start: p, end: keys.PrefixEnd(p)}
}

// interval is one elementary interval of the claim map: every key in it
// resolves, by longest match, to the same claim.
type interval struct {
	span
	owner      Owner
	mode       ClaimMode
	serialized bool
}

// claimIndex resolves an absolute legacy key to its claim by longest match.
// It is built once from every owner's claims, enabled or not, so a disabled
// module's claims stay reserved (§5S.4).
type claimIndex struct {
	intervals []interval           // sorted, disjoint
	byOwner   map[Owner][]interval // the same intervals, per owner
}

// resolve returns the interval holding k, or false when no claim does.
func (ci *claimIndex) resolve(k []byte) (interval, bool) {
	i := sort.Search(len(ci.intervals), func(i int) bool { return bytes.Compare(ci.intervals[i].end, k) > 0 })
	if i < len(ci.intervals) && ci.intervals[i].contains(k) {
		return ci.intervals[i], true
	}
	return interval{}, false
}

// buildClaimIndex computes the elementary intervals of every claim: between
// consecutive span boundaries every key resolves to the deepest claim that
// contains the interval.
func buildClaimIndex(refs []claimRef) *claimIndex {
	var points [][]byte
	for _, r := range refs {
		points = append(points, r.sp.start, r.sp.end)
	}
	slices.SortFunc(points, bytes.Compare)
	points = slices.CompactFunc(points, bytes.Equal)
	ci := &claimIndex{byOwner: make(map[Owner][]interval)}
	for i := 0; i+1 < len(points); i++ {
		lo, hi := points[i], points[i+1]
		best := -1
		for j, r := range refs {
			if bytes.Compare(r.sp.start, lo) <= 0 && bytes.Compare(hi, r.sp.end) <= 0 {
				if best < 0 || r.depth > refs[best].depth {
					best = j
				}
			}
		}
		if best < 0 {
			continue
		}
		r := refs[best]
		iv := interval{start: lo, end: hi, owner: r.owner, mode: r.c.Mode, serialized: r.c.Serialized}
		if n := len(ci.intervals); n > 0 {
			last := &ci.intervals[n-1]
			if bytes.Equal(last.end, lo) && last.owner == iv.owner && last.mode == iv.mode && last.serialized == iv.serialized {
				last.end = hi
				continue
			}
		}
		ci.intervals = append(ci.intervals, iv)
	}
	for _, iv := range ci.intervals {
		ci.byOwner[iv.owner] = append(ci.byOwner[iv.owner], iv)
	}
	return ci
}

// validateClaims is kernel validation items 2 and 3 of §5S.3.4 over every
// owner's claims, plus the claim's own shape. It returns every problem.
// claimSeparators are the bytes a claim prefix may end in (§5S.3.4).
const claimSeparators = ":/_."

func validateClaims(refs []claimRef) []string {
	var probs []string
	reserved := []string{"m/", "c/"}
	for _, r := range refs {
		c := r.c
		switch {
		case (c.Prefix == "") == (c.Key == ""):
			probs = append(probs, fmt.Sprintf("%s: a claim sets exactly one of Prefix or Key (Prefix %q, Key %q)", r.owner, c.Prefix, c.Key))
			continue
		case c.Mode == ClaimInvalid || c.Mode > ClaimRetired:
			probs = append(probs, fmt.Sprintf("%s: Mode %s is not a claim mode", r.name(), c.Mode))
		}
		if p := c.Prefix; p != "" {
			// The separator rule: "joker" would also claim "jokerized:".
			// A prefix of only 0xff bytes has no PrefixEnd, so its span would
			// be open-ended; the rule already refuses it (0xff is no
			// separator), and the check stays explicit for any later
			// exception to the rule.
			if !strings.ContainsRune(claimSeparators, rune(p[len(p)-1])) {
				probs = append(probs, fmt.Sprintf("%s: a claim prefix ends in a separator byte (one of %q)", r.name(), claimSeparators))
			}
			if keys.PrefixEnd([]byte(p)) == nil {
				probs = append(probs, fmt.Sprintf("%s: a prefix of only 0xff bytes has no end", r.name()))
			}
		}
		t := r.target()
		for _, res := range reserved {
			if strings.HasPrefix(t, res) || strings.HasPrefix(res, t) {
				probs = append(probs, fmt.Sprintf("%s: claims under or above the reserved %q (item 2)", r.name(), res))
			}
		}
	}
	// Item 3: overlap by containment, longest match.
	for i, a := range refs {
		for j, b := range refs {
			if i >= j {
				continue
			}
			if a.target() == b.target() && (a.c.Key != "") == (b.c.Key != "") {
				probs = append(probs, fmt.Sprintf("%s and %s claim the same %s (item 3)", a.name(), b.name(), kindOf(a)))
				continue
			}
			outer, inner, nested := nesting(a, b)
			if !nested || outer.owner == inner.owner {
				continue
			}
			if !slices.Contains(outer.c.Except, inner.target()) {
				probs = append(probs, fmt.Sprintf("%s overlaps %s without listing %q in Except (item 3)", outer.name(), inner.name(), inner.target()))
			}
		}
	}
	// Every Except entry is claimed by exactly one other owner.
	for _, a := range refs {
		for _, ex := range a.c.Except {
			var others, same int
			for _, b := range refs {
				if b.target() != ex {
					continue
				}
				if b.owner == a.owner {
					same++
				} else {
					others++
				}
			}
			switch {
			case same > 0:
				probs = append(probs, fmt.Sprintf("%s lists its own claim %q in Except: Except is for cross-owner carve-outs only", a.name(), ex))
			case others != 1:
				probs = append(probs, fmt.Sprintf("%s lists %q in Except, which %d other owners claim (want exactly 1)", a.name(), ex, others))
			}
		}
	}
	return probs
}

// nesting reports whether one claim's span holds the other's: outer is the
// containing claim.
func nesting(a, b claimRef) (outer, inner claimRef, nested bool) {
	if bytes.Compare(a.sp.start, b.sp.start) <= 0 && bytes.Compare(b.sp.end, a.sp.end) <= 0 {
		return a, b, true
	}
	if bytes.Compare(b.sp.start, a.sp.start) <= 0 && bytes.Compare(a.sp.end, b.sp.end) <= 0 {
		return b, a, true
	}
	return claimRef{}, claimRef{}, false
}

func kindOf(r claimRef) string {
	if r.c.Key != "" {
		return "key"
	}
	return "prefix"
}

// ledgerClaim is one claim as the claim ledger records it.
type ledgerClaim struct {
	Prefix     string   `json:"prefix,omitempty"`
	Key        string   `json:"key,omitempty"`
	Except     []string `json:"except,omitempty"`
	Mode       string   `json:"mode"`
	Serialized bool     `json:"serialized,omitempty"`
}

// claimLedger is c/storage/claims/<owner> = {claims, modes (by name), build,
// at} (§5S.3.4 rule 5).
type claimLedger struct {
	Claims []ledgerClaim `json:"claims"`
	Build  string        `json:"build"`
	At     string        `json:"at"`
}

func ledgerOf(claims []Claim) []ledgerClaim {
	out := make([]ledgerClaim, 0, len(claims))
	for _, c := range claims {
		out = append(out, ledgerClaim{Prefix: c.Prefix, Key: c.Key, Except: c.Except, Mode: c.Mode.String(), Serialized: c.Serialized})
	}
	return out
}

func sameLedgerClaims(a, b []ledgerClaim) bool {
	return slices.EqualFunc(a, b, func(x, y ledgerClaim) bool {
		return x.Prefix == y.Prefix && x.Key == y.Key && x.Mode == y.Mode && x.Serialized == y.Serialized &&
			slices.Equal(x.Except, y.Except)
	})
}

// orphanProblems is rule 5: a claim on disk whose owner is absent from the
// build is orphaned, and a build claim over its range must name that owner in
// TakesOver.
func orphanProblems(refs []claimRef, ledgers map[Owner]claimLedger, inBuild map[Owner]bool) []string {
	var probs []string
	for former, l := range ledgers {
		if inBuild[former] {
			continue
		}
		for _, lc := range l.Claims {
			orphan := claimRef{owner: former, c: Claim{Prefix: lc.Prefix, Key: lc.Key}}
			orphan.sp = claimSpan(orphan.c)
			for _, r := range refs {
				if !r.sp.overlaps(orphan.sp.start, orphan.sp.end) {
					continue
				}
				if r.c.TakesOver != string(former) {
					probs = append(probs, fmt.Sprintf("%s overlaps the orphaned claim %q of removed owner %s; set TakesOver: %q to take it over (rule 5)",
						r.name(), orphan.target(), former, former))
				}
			}
		}
	}
	slices.Sort(probs)
	return probs
}
