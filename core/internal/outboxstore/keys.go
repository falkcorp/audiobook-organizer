// file: core/internal/outboxstore/keys.go
// version: 1.0.1
// guid: 47a4a283-dcad-4da8-b454-90f24d1d624d
// last-edited: 2026-11-12

// Package outboxstore is the key layout, the record formats and the raw
// staging of core's durable outbox, the c/outbox/ keyspace (05-events §5E.5.2).
// It knows keys, versioned records and the guard protocol that keeps the
// relay's writers from losing an update, and nothing about topics' meaning,
// handlers, retries or tasks: package outbox holds every rule about what a
// write means. outboxstore never imports outbox or tasks, so both can call
// into it without a cycle.
//
// Every key is a keys.Join tuple; the table's "/" separates segments:
//
//	in/<topic>/<ulid>                       staged message awaiting intake (Stage, in the producer's batch)
//	m/<topic>/<pp>/<seq20>                  sequenced message (pp: partition, 2 digits; seq20: zero-padded)
//	seq/<topic>                             the topic's high-water mark (Serialized; the relay only)
//	cur/<consumer>/<topic>/<pp>             a consumer's cursor on one partition (Serialized)
//	try/<consumer>/<topic>/<pp>             the messages of that pair under retry
//	dead/<consumer>/<topic>/<seq20>         dead-lettered message, error token and attempts (Serialized)
//
// The Serialized sub-prefixes are exactly seq/, cur/ and dead/. Every write of
// a pair's state (its cursor, its try record) rides in a batch that read the
// pair's cursor with GetForUpdate and rewrote it with a higher Rev, so two
// writers of one pair (a zombie relay, an operator's purge racing a delivery)
// can never both commit: the second's precondition fails and nothing it staged
// is applied. A dead letter is guarded by its own key, not by a cursor: the
// relay writes it behind a GetForUpdate of the key, and an operator's retry or
// discard reads it through LoadDead, so two operators on one letter, or an
// operator and the relay, can never both commit, whether or not the pair still
// has a cursor row.
//
// The rows hold every value as a codec.JSON record of version 1. A row of
// another version is never skipped, defaulted or marked done: decoding it
// returns ErrUnknownVersion, and the caller stops that topic or pair and says
// so (a newer build wrote it).
package outboxstore

import (
	"fmt"
	"hash/fnv"
	"strconv"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the storage owner of the c/outbox/ keyspace the kernel registers.
const Owner storage.Owner = "core/outbox"

// Family segments: the first segment of every key.
const (
	famIn   = "in"
	famMsg  = "m"
	famSeq  = "seq"
	famCur  = "cur"
	famTry  = "try"
	famDead = "dead"
)

// MaxPartitions is the most partitions a topic may declare: pp is two digits.
const MaxPartitions = 99

// DefaultPartitions is Durable.Partitions when it is zero (05-events §5E.4).
const DefaultPartitions = 16

// StorageSpec is the c/outbox/ spec the kernel registers for Owner: a core
// keyspace whose Serialized sub-prefixes are exactly seq/, cur/ and dead/.
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: SerializedPrefixes()}
}

// SerializedPrefixes are the relative Serialized sub-prefixes: seq/, cur/ and
// dead/.
func SerializedPrefixes() []string {
	return []string{
		string(keys.Prefix(keys.Str(famSeq))),
		string(keys.Prefix(keys.Str(famCur))),
		string(keys.Prefix(keys.Str(famDead))),
	}
}

// Partitions resolves a topic's declared partition count: 0 is the default.
func Partitions(declared int) int {
	if declared == 0 {
		return DefaultPartitions
	}
	return declared
}

// ValidPartitions reports whether a resolved partition count is usable.
func ValidPartitions(n int) bool { return n >= 1 && n <= MaxPartitions }

// Partition is fnv1a32(key) % partitions (05-events §5E.5.2).
func Partition(key string, partitions int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(partitions))
}

func seq20(seq uint64) keys.Seg { return keys.Str(fmt.Sprintf("%020d", seq)) }
func pp2(pp int) keys.Seg       { return keys.Str(fmt.Sprintf("%02d", pp)) }

// InKey is in/<topic>/<ulid>.
func InKey(topic, id string) []byte {
	return keys.Join(keys.Str(famIn), keys.Str(topic), keys.Str(id))
}

// InPrefix is in/<topic>/.
func InPrefix(topic string) []byte { return keys.Prefix(keys.Str(famIn), keys.Str(topic)) }

// InRoot is the in/ family.
func InRoot() []byte { return keys.Prefix(keys.Str(famIn)) }

// MsgKey is m/<topic>/<pp>/<seq20>.
func MsgKey(topic string, pp int, seq uint64) []byte {
	return keys.Join(keys.Str(famMsg), keys.Str(topic), pp2(pp), seq20(seq))
}

// MsgPrefix is m/<topic>/<pp>/.
func MsgPrefix(topic string, pp int) []byte {
	return keys.Prefix(keys.Str(famMsg), keys.Str(topic), pp2(pp))
}

// SeqKey is seq/<topic>.
func SeqKey(topic string) []byte { return keys.Join(keys.Str(famSeq), keys.Str(topic)) }

// CurKey is cur/<consumer>/<topic>/<pp>.
func CurKey(consumer, topic string, pp int) []byte {
	return keys.Join(keys.Str(famCur), keys.Str(consumer), keys.Str(topic), pp2(pp))
}

// CurRoot is the cur/ family.
func CurRoot() []byte { return keys.Prefix(keys.Str(famCur)) }

// TryKey is try/<consumer>/<topic>/<pp>.
func TryKey(consumer, topic string, pp int) []byte {
	return keys.Join(keys.Str(famTry), keys.Str(consumer), keys.Str(topic), pp2(pp))
}

// DeadKey is dead/<consumer>/<topic>/<seq20>.
func DeadKey(consumer, topic string, seq uint64) []byte {
	return keys.Join(keys.Str(famDead), keys.Str(consumer), keys.Str(topic), seq20(seq))
}

// DeadPrefix is dead/<consumer>/.
func DeadPrefix(consumer string) []byte {
	return keys.Prefix(keys.Str(famDead), keys.Str(consumer))
}

// DeadRoot is the dead/ family.
func DeadRoot() []byte { return keys.Prefix(keys.Str(famDead)) }

// splitKey splits rel into its segments and checks the family and arity.
func splitKey(rel []byte, family string, n int) ([]string, error) {
	segs, err := keys.Split(rel)
	if err != nil {
		return nil, err
	}
	if len(segs) != n || string(segs[0]) != family {
		return nil, fmt.Errorf("outboxstore: %q is not a %s key of %d segments", rel, family, n)
	}
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = string(s)
	}
	return out, nil
}

func parseSeq(s string) (uint64, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("outboxstore: seq %q: %w", s, err)
	}
	return n, nil
}

func parsePP(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n >= MaxPartitions+1 {
		return 0, fmt.Errorf("outboxstore: partition %q", s)
	}
	return n, nil
}

// ParseInKey splits in/<topic>/<ulid>.
func ParseInKey(rel []byte) (topic, id string, err error) {
	s, err := splitKey(rel, famIn, 3)
	if err != nil {
		return "", "", err
	}
	return s[1], s[2], nil
}

// ParseMsgKey splits m/<topic>/<pp>/<seq20>.
func ParseMsgKey(rel []byte) (topic string, pp int, seq uint64, err error) {
	s, err := splitKey(rel, famMsg, 4)
	if err != nil {
		return "", 0, 0, err
	}
	if pp, err = parsePP(s[2]); err != nil {
		return "", 0, 0, err
	}
	if seq, err = parseSeq(s[3]); err != nil {
		return "", 0, 0, err
	}
	return s[1], pp, seq, nil
}

// ParseCurKey splits cur/<consumer>/<topic>/<pp>.
func ParseCurKey(rel []byte) (consumer, topic string, pp int, err error) {
	s, err := splitKey(rel, famCur, 4)
	if err != nil {
		return "", "", 0, err
	}
	if pp, err = parsePP(s[3]); err != nil {
		return "", "", 0, err
	}
	return s[1], s[2], pp, nil
}

// ParseDeadKey splits dead/<consumer>/<topic>/<seq20>.
func ParseDeadKey(rel []byte) (consumer, topic string, seq uint64, err error) {
	s, err := splitKey(rel, famDead, 4)
	if err != nil {
		return "", "", 0, err
	}
	if seq, err = parseSeq(s[3]); err != nil {
		return "", "", 0, err
	}
	return s[1], s[2], seq, nil
}
