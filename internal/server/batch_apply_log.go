// file: internal/server/batch_apply_log.go
// version: 1.1.0
// guid: ca44bf85-18ad-4a64-96ce-f9f9d87aaf31
// last-edited: 2026-09-28
//
// Per-book outcome lines and the count-bearing progress label for the
// metadata.batch-apply-cached op log.

package server

import (
	"fmt"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// batchApplyLabelEvery is how many examined books sit between rebuilds of the
// batch apply's progress label (see candidateFetchProgressEvery for why the
// count-bearing message is not rebuilt per book).
const batchApplyLabelEvery = 25

// batchApplyLabel caches the progress label and rebuilds it every
// batchApplyLabelEvery examined books. RunItems calls Label from its worker
// goroutines, so get is safe for concurrent use.
type batchApplyLabel struct {
	build func() string

	mu     sync.Mutex
	msg    string
	builtN int64
}

func newBatchApplyLabel(build func() string) *batchApplyLabel {
	return &batchApplyLabel{build: build}
}

// get returns the label for n examined books.
func (l *batchApplyLabel) get(n int64) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.msg == "" || n-l.builtN >= batchApplyLabelEvery {
		l.msg, l.builtN = l.build(), n
	}
	return l.msg
}

// batchApplyBookLabel renders the book an outcome is about: its title and
// author when the plan read the book, else its id.
func batchApplyBookLabel(id string, out applyOutcome) string {
	if out.BookTitle == "" && out.BookAuthor == "" {
		return "book " + opLogQuoted(id)
	}
	return opLogBookRef(out.BookTitle, out.BookAuthor, id, "")
}

// batchApplyCandidateLabel renders the candidate an outcome is about, e.g.
// `"Dune" by "Frank Herbert" (Audible, score 0.93)`, or "" when none.
func batchApplyCandidateLabel(out applyOutcome) string {
	c := out.Candidate
	if c == nil {
		return ""
	}
	if c.Source == "" {
		return fmt.Sprintf("%s (score %.2f)", opLogBookLabel(c.Title, c.Author), c.Score)
	}
	return fmt.Sprintf("%s (%s, score %.2f)", opLogBookLabel(c.Title, c.Author), logger.SanitizeLogValue(c.Source), c.Score)
}

// batchApplyAppliedLine is the op-log line for an applied book.
func batchApplyAppliedLine(id string, out applyOutcome) string {
	var b strings.Builder
	b.WriteString("applied: ")
	b.WriteString(batchApplyBookLabel(id, out))
	if c := batchApplyCandidateLabel(out); c != "" {
		b.WriteString(" → ")
		b.WriteString(c)
	}
	b.WriteString(batchApplyIdentityEvidence(out))
	if out.OwnerReviewed {
		b.WriteString("; owner-reviewed over the certainty gate")
	}
	if out.OwnerReplace {
		b.WriteString("; owner replace: existing values overwritten")
	}
	if len(out.SkippedLocked) > 0 {
		b.WriteString("; user-locked fields left unchanged: ")
		b.WriteString(logger.SanitizeLogValue(strings.Join(out.SkippedLocked, ", ")))
	}
	return b.String()
}

// batchApplyIdentityEvidence renders the identity evidence the certainty gate
// accepted for the candidate (applygate.EvaluateTranscribed), e.g. `; identity:
// found by searching the transcribed title "Planet Hulk", which the
// candidate's title matches`, or "" when there was none. It is how an applied
// book with a blank or chapter-number title says why it passed.
func batchApplyIdentityEvidence(out applyOutcome) string {
	var ev *metafetch.CandidateIdentityEvidence
	if out.Gate != nil {
		ev = out.Gate.IdentityEvidence
	}
	if ev == nil && out.Candidate != nil {
		ev = out.Candidate.IdentityEvidence
	}
	if ev == nil {
		return ""
	}
	return "; identity: " + logger.SanitizeLogValue(ev.Detail)
}

// batchApplyRefusedLine is the op-log line for a book that was not applied:
// the reason, the certainty gate's refusal and score when the gate ran, and
// the error when there is one.
func batchApplyRefusedLine(id string, out applyOutcome) string {
	var b strings.Builder
	b.WriteString("not applied: ")
	b.WriteString(batchApplyBookLabel(id, out))
	if c := batchApplyCandidateLabel(out); c != "" {
		b.WriteString(" → ")
		b.WriteString(c)
	}
	b.WriteString(" — ")
	b.WriteString(logger.SanitizeLogValue(out.Reason))
	if out.Gate != nil && out.Gate.Reason != "" {
		fmt.Fprintf(&b, "; gate: %s", logger.SanitizeLogValue(out.Gate.Reason))
		if out.Gate.Detail != "" {
			fmt.Fprintf(&b, " (%s)", logger.SanitizeLogValue(out.Gate.Detail))
		}
	}
	if out.Err != nil {
		fmt.Fprintf(&b, ": %s", logger.SanitizeLogValue(out.Err.Error()))
	}
	b.WriteString(batchApplyIdentityEvidence(out))
	return b.String()
}
