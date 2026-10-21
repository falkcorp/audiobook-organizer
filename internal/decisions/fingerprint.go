// file: internal/decisions/fingerprint.go
// version: 1.0.0
// guid: 5e17c9a4-2b68-4f03-8d71-a4c0e93b6f28
// last-edited: 2026-10-21

package decisions

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
)

// The two hashes that identify a proposal.
//
// STABILITY IS THE DELIVERABLE. A stored verdict is found again only if the
// same proposal hashes to the same string next year, so the byte encoding
// below is frozen: any change orphans every reject ever recorded. The golden
// vectors in fingerprint_test.go pin it. To change the scheme, bump the domain
// tag AND keep reading the old one; never edit these functions in place.
const (
	subjectDomain  = "decisions/subject/v1"
	materialDomain = "decisions/material/v1"
)

// hasher writes length-prefixed fields, so no concatenation of one set of
// fields can equal another ("ab"+"c" is not "a"+"bc").
type hasher struct{ buf []byte }

func newHasher(domain string) *hasher {
	h := &hasher{}
	h.str(domain)
	return h
}

func (h *hasher) str(s string) {
	h.buf = binary.AppendUvarint(h.buf, uint64(len(s)))
	h.buf = append(h.buf, s...)
}

func (h *hasher) count(n int) { h.buf = binary.AppendUvarint(h.buf, uint64(n)) }

func (h *hasher) sum() string {
	d := sha256.Sum256(h.buf)
	return hex.EncodeToString(d[:])
}

// SubjectKey is "the same proposal": op_kind, entity type, the SET of entity
// ids (order and duplicates do not matter) and an op-defined scope that
// separates different proposals about the same entities (a metadata source and
// candidate, a repairs fixer id and row id). It is 64 lowercase hex characters.
func SubjectKey(opKind, entityType string, entityIDs []string, scope string) string {
	h := newHasher(subjectDomain)
	h.str(opKind)
	h.str(entityType)
	ids := sortedUnique(entityIDs)
	h.count(len(ids))
	for _, id := range ids {
		h.str(id)
	}
	h.str(scope)
	return h.sum()
}

// MaterialHash hashes only the decision-relevant evidence of a proposal (for a
// field-rewrite fixer, the value it would write). Map order does not matter.
// A nil or empty map returns "": the proposal has no material evidence, its
// verdict is sticky and can never be "reopened" by changed data.
func MaterialHash(proposed map[string]string) string {
	if len(proposed) == 0 {
		return ""
	}
	keys := make([]string, 0, len(proposed))
	for k := range proposed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := newHasher(materialDomain)
	h.count(len(keys))
	for _, k := range keys {
		h.str(k)
		h.str(proposed[k])
	}
	return h.sum()
}

func sortedUnique(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if i > 0 && s == out[n-1] {
			continue
		}
		out[n] = s
		n++
	}
	return out[:n]
}
