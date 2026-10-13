// file: internal/operations/opswriter/intent.go
// version: 1.0.0
// guid: fe6fcac0-ca20-4243-81b3-c0efd1ca5d30
// last-edited: 2026-10-13

package opswriter

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// IntentStore is the write-ahead surface (database.OpsV3AuxStore's intents).
type IntentStore interface {
	PutIntentV3(id string, in database.Intent) error
	ClearIntentV3(id string, seq uint64) error
	ListIntentsV3(id string) ([]database.Intent, error)
}

// maxPlanned bounds the Preview plan lines one Writer keeps in memory.
const maxPlanned = 10_000

// intents numbers and writes one run's write-ahead rows, and keeps the plan
// lines of a Preview run.
//
// The sequence is seeded from the run's outstanding intents the first time
// it is needed. A resumed attempt therefore numbers its intents after the
// ones a crashed attempt left behind and never overwrites (and then clears)
// that evidence of a partially applied write.
type intents struct {
	store IntentStore
	opID  string

	mu      sync.Mutex
	seeded  bool
	next    uint64
	planned []database.Intent
	dropped int
}

// intentPlan is an intent's Planned payload.
type intentPlan struct {
	Op     string `json:"op"`
	Detail string `json:"detail,omitempty"`
}

func encodePlan(op, detail string) json.RawMessage {
	b, err := json.Marshal(intentPlan{Op: op, Detail: detail})
	if err != nil { // two strings cannot fail to encode
		return nil
	}
	return b
}

// put writes an intent before a write and returns its seq. With no store
// (internal/repairs journals first itself) it writes nothing and returns 0.
func (in *intents) put(subject, kind, op, detail string) (uint64, error) {
	if in.store == nil {
		return 0, nil
	}
	in.mu.Lock()
	if !in.seeded {
		prev, err := in.store.ListIntentsV3(in.opID)
		if err != nil {
			in.mu.Unlock()
			return 0, fmt.Errorf("read outstanding intents: %w", err)
		}
		for _, p := range prev {
			in.next = max(in.next, p.Seq)
		}
		in.seeded = true
	}
	in.next++
	seq := in.next
	in.mu.Unlock()
	err := in.store.PutIntentV3(in.opID, database.Intent{Seq: seq, Subject: subject, Kind: kind,
		Planned: encodePlan(op, detail), WrittenAt: time.Now().UTC()})
	if err != nil {
		return 0, fmt.Errorf("write intent: %w", err)
	}
	return seq, nil
}

// clear deletes the intent of a finished write. A failure leaves the intent,
// which recovery reports as "may be partially applied": conservative, never
// wrong in the dangerous direction.
func (in *intents) clear(seq uint64) error {
	if in.store == nil || seq == 0 {
		return nil
	}
	return in.store.ClearIntentV3(in.opID, seq)
}

// plan records a Preview write as a plan line (bounded).
func (in *intents) plan(subject, kind, op, detail string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.planned) >= maxPlanned {
		in.dropped++
		return
	}
	in.planned = append(in.planned, database.Intent{Seq: uint64(len(in.planned) + 1), Subject: subject,
		Kind: kind, Planned: encodePlan(op, detail), WrittenAt: time.Now().UTC()})
}

// plannedLines returns the plan lines and how many were dropped past the cap.
func (in *intents) plannedLines() ([]database.Intent, int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.planned), in.dropped
}
