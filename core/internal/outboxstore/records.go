// file: core/internal/outboxstore/records.go
// version: 1.0.2
// guid: 5f394976-e7d8-4f5e-be83-5afd62ff5595
// last-edited: 2026-11-12

package outboxstore

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/storage/codec"
)

// RecordVersion is the version of every record this package writes. A row of
// another version decodes to codec.ErrUnknownVersion and is never skipped.
const RecordVersion = 1

// Msg is a message as the outbox keeps it: the same fields in an intake row,
// a sequenced row and a dead letter.
type Msg struct {
	// ID is the message's IdempotencyKey: the ULID Stage assigned, the same on
	// every redelivery and on a retried dead letter.
	ID       string          `json:"id"`
	Key      string          `json:"key"`
	Payload  json.RawMessage `json:"payload"`
	StagedAt time.Time       `json:"staged_at"`
	// Producer is the module that staged it ("core" for core).
	Producer string `json:"producer"`
	// To, when set, names the one consumer a retried dead letter is for;
	// every other consumer acks it without running (05-events §5E.11).
	To string `json:"to,omitempty"`
}

// Intake is the value of in/<topic>/<ulid>.
type Intake struct {
	Msg
	// NotBefore delays intake: a row is sequenced only once it is due.
	NotBefore time.Time `json:"not_before,omitzero"`
}

// High is the value of seq/<topic>: the last seq issued and the partition
// count the topic's keys were spread over, stamped by the batch that issued
// them, so a changed declaration is seen and refused instead of reordering
// keys.
type High struct {
	High       uint64 `json:"high"`
	Partitions int    `json:"partitions"`
}

// Cursor is the value of cur/<consumer>/<topic>/<pp>. Every message of the
// partition at or below Seq has been taken over by the consumer: acked,
// filtered or dead-lettered. Rev counts the batches that touched the pair's
// state (cursor, try, dead letters); each one reads the cursor with
// GetForUpdate and writes it back with Rev+1, so a stale writer's commit
// fails.
//
// At is when the pair's state was last written (StagePair sets it to the
// commit's clock). It is audit data for someone reading the store: no decision
// reads it (the orphan grace runs from Absent, not from At).
//
// Absent is when retention first saw the cursor's consumer in neither the
// registered nor the declared set (a consumer removed from the build); the zero
// time is every consumer that is present. It is the start of the orphan grace
// period: only retention writes it, only for a consumer that is gone, and it
// clears it again as soon as the consumer is back.
type Cursor struct {
	Seq    uint64    `json:"seq"`
	Rev    uint64    `json:"rev"`
	At     time.Time `json:"at"`
	Absent time.Time `json:"absent_since,omitzero"`
}

// TryEntry is one message under retry: the head message of an ordered
// consumer's partition, or any failed message of an unordered one.
type TryEntry struct {
	Seq     uint64    `json:"seq"`
	Attempt int       `json:"attempt"` // deliveries so far, all failed
	NextAt  time.Time `json:"next_at"`
	Token   string    `json:"last_error_token"`
}

// Try is the value of try/<consumer>/<topic>/<pp>, in seq order.
type Try struct {
	Entries []TryEntry `json:"entries"`
}

// Dead is the value of dead/<consumer>/<topic>/<seq20>.
type Dead struct {
	Msg      Msg       `json:"msg"`
	Part     int       `json:"partition"`
	Token    string    `json:"token"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
}

func intakeCodec() codec.Codec[Intake] { return codec.JSON[Intake](RecordVersion) }
func msgCodec() codec.Codec[Msg]       { return codec.JSON[Msg](RecordVersion) }
func highCodec() codec.Codec[High]     { return codec.JSON[High](RecordVersion) }
func curCodec() codec.Codec[Cursor]    { return codec.JSON[Cursor](RecordVersion) }
func tryCodec() codec.Codec[Try]       { return codec.JSON[Try](RecordVersion) }
func deadCodec() codec.Codec[Dead]     { return codec.JSON[Dead](RecordVersion) }
