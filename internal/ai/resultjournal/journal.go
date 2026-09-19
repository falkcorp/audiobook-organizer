// file: internal/ai/resultjournal/journal.go
// version: 1.2.0
// guid: 10003ffd-5d00-48b1-865a-37ac9f9ae826
// last-edited: 2026-09-19

// Package resultjournal is a durable, content-keyed cache of finished AI
// results (whisper transcripts first; LLM results later). It exists so a
// restart or deploy in the middle of a long AI op never throws away work a
// model already finished: every result is written here the moment it comes
// back, and a re-run serves it from the journal instead of sending it again.
//
// # Two separate things, never one key
//
// The journal answers ONE question: "has this exact input already been run
// through this exact model?" It is keyed by CONTENT (see ContentKey), so two
// books whose inputs are byte-identical -- the shared Audible intro clip is the
// common case -- share one entry. That sharing is the point: it is free dedup of
// model work.
//
// It deliberately records NOTHING about whether a result was APPLIED to a book.
// A content-keyed "applied" flag would let book A's apply suppress book B's
// when their clips are identical. Per-book idempotency is read from the book row
// itself by the caller (e.g. the transcript text + status already stored).
//
// # Durability and concurrency
//
// The journal is stateless over its store: no in-memory map, no locks. Every
// method is one or more store calls, so it is exactly as concurrency-safe as the
// store (PebbleStore is). Complete returns only after the store's SetRaw has
// returned; on PebbleStore that is a pebble.Sync write, so the entry survives a
// kill -9 the instant Complete returns.
package resultjournal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var journalLog = logger.New("resultjournal")

// keyPrefix is the root of every journal key: aijournal:<kind>:<contentKey>.
const keyPrefix = "aijournal:"

// KVStore is the slice of the store the journal needs: the raw key-value escape
// hatch (database.RawKVStore). Narrow on purpose -- the journal has no business
// with books, files or anything else on database.Store. It deliberately omits
// the unbounded ScanPrefix: the journal grows with every distinct clip ever
// transcribed (up to ~742k entries), so Prune pages through ScanPrefixPage.
type KVStore interface {
	SetRaw(key string, value []byte) error
	GetRaw(key string) ([]byte, error)
	ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error)
	DeleteRawBatch(keys []string) error
}

// defaultPrunePageSize bounds Prune's memory: at most this many entries
// (~1-2 KB each) are held at once, and each page's deletions go down in one
// synced batch.
const defaultPrunePageSize = 500

// Entry is the stored value for one finished result.
type Entry struct {
	// Endpoint is the server that produced the result (diagnostic only; it is
	// NOT part of the key, so a result from one endpoint serves any endpoint
	// running the same model).
	Endpoint string `json:"endpoint"`
	// Model is the model identity the result came from. It IS part of the key
	// (callers fold it into ContentKey); it is repeated here so an entry is
	// self-describing when inspected.
	Model string `json:"model"`
	// At is when the result was journalled; Prune ages entries by it.
	At time.Time `json:"at"`
	// Result is the caller's result, verbatim JSON.
	Result json.RawMessage `json:"result"`
}

// Journal is a result cache for one kind of AI work ("whisper", ...).
type Journal struct {
	store    KVStore
	kind     string
	prefix   string
	now      func() time.Time
	pageSize int
}

// New returns a journal for kind over store. kind must be non-empty and must
// not contain ':' (it is a key segment).
func New(store KVStore, kind string) (*Journal, error) {
	if store == nil {
		return nil, errors.New("resultjournal: nil store")
	}
	if kind == "" || strings.Contains(kind, ":") {
		return nil, fmt.Errorf("resultjournal: invalid kind %q", kind)
	}
	return &Journal{store: store, kind: kind, prefix: keyPrefix + kind + ":", now: time.Now, pageSize: defaultPrunePageSize}, nil
}

// ContentKey hashes the parts that together identify an input+model+params
// combination into a fixed-length hex key. Each part is length-prefixed so
// ("ab","c") and ("a","bc") cannot collide.
func ContentKey(parts ...string) string {
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range parts {
		n := uint64(len(p))
		for i := range lenBuf {
			lenBuf[i] = byte(n >> (8 * i))
		}
		h.Write(lenBuf[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (j *Journal) storeKey(contentKey string) string { return j.prefix + contentKey }

// Lookup returns the journalled result for contentKey. ok=false with a nil
// error is a plain miss. An entry that exists but cannot be decoded is
// returned as an error, never as a miss, so a corrupt journal is visible
// rather than silently re-running work.
func (j *Journal) Lookup(contentKey string) (result json.RawMessage, ok bool, err error) {
	raw, err := j.store.GetRaw(j.storeKey(contentKey))
	if err != nil {
		return nil, false, fmt.Errorf("resultjournal: get %s: %w", contentKey, err)
	}
	if raw == nil {
		return nil, false, nil
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, false, fmt.Errorf("resultjournal: decode %s: %w", contentKey, err)
	}
	if len(e.Result) == 0 {
		return nil, false, fmt.Errorf("resultjournal: entry %s has no result", contentKey)
	}
	return e.Result, true, nil
}

// Complete journals result under contentKey. It returns only after the
// store's write has returned, so the result is durable (on PebbleStore, fsynced)
// before the caller reports progress or moves on. A later Complete for the same
// key overwrites the earlier one.
func (j *Journal) Complete(contentKey, endpoint, model string, result any) error {
	res, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("resultjournal: encode result %s: %w", contentKey, err)
	}
	val, err := json.Marshal(Entry{Endpoint: endpoint, Model: model, At: j.now().UTC(), Result: res})
	if err != nil {
		return fmt.Errorf("resultjournal: encode entry %s: %w", contentKey, err)
	}
	if err := j.store.SetRaw(j.storeKey(contentKey), val); err != nil {
		return fmt.Errorf("resultjournal: set %s: %w", contentKey, err)
	}
	return nil
}

// Prune deletes this kind's entries journalled more than olderThan ago and
// returns how many it deleted. An entry that cannot be decoded is corrupt: it
// can never be served (Lookup errors on it), so it is deleted too and logged,
// not skipped.
//
// It touches ONLY this journal's kind: the scan is bounded to
// "aijournal:<kind>:", so other kinds sharing the aijournal: root (llm,
// batch -- the batch poller's keys, #3454 -- ...) are never read or deleted.
//
// Memory is bounded by the page size, not by the journal: it walks the kind's
// keyspace one page at a time and deletes each page's expired keys in one
// batch. The cursor is the last key SCANNED, so a page that deletes nothing
// still advances, and deleting behind a forward-moving exclusive cursor never
// skips a key.
//
// olderThan must be positive; "keep forever" is the caller's decision not to
// call Prune, never a zero duration that would delete everything. ctx is
// checked before every page: on cancellation Prune returns the count deleted
// so far with ctx's error, and every deleted batch has already committed.
func (j *Journal) Prune(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, fmt.Errorf("resultjournal: prune retention must be positive, got %v", olderThan)
	}
	cutoff := j.now().Add(-olderThan)
	deleted := 0
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		pairs, next, err := j.store.ScanPrefixPage(j.prefix, after, j.pageSize)
		if err != nil {
			return deleted, fmt.Errorf("resultjournal: scan %s after %q: %w", j.prefix, after, err)
		}
		var doomed []string
		for _, kv := range pairs {
			var e Entry
			if uerr := json.Unmarshal(kv.Value, &e); uerr != nil {
				journalLog.Warn("deleting undecodable entry: key=%s err=%v", kv.Key, uerr)
			} else if !e.At.Before(cutoff) {
				continue
			}
			doomed = append(doomed, kv.Key)
		}
		if len(doomed) > 0 {
			if err := j.store.DeleteRawBatch(doomed); err != nil {
				return deleted, fmt.Errorf("resultjournal: delete %d entries from %s: %w", len(doomed), doomed[0], err)
			}
			deleted += len(doomed)
		}
		if next == "" {
			return deleted, nil
		}
		after = next
	}
}
