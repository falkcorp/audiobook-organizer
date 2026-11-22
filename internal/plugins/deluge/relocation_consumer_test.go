// file: internal/plugins/deluge/relocation_consumer_test.go
// version: 0.1.0
// guid: 09b193a2-e195-4f85-8839-0dfb6f7fbbeb
// last-edited: 2026-11-20

package deluge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// newRelocationOutbox builds an outbox over an in-memory core store whose
// catalog holds books.book.relocated.v1, declared by books. The owner and
// spec mirror core/internal/outboxstore's (the root module cannot import it).
func newRelocationOutbox(t *testing.T) *outbox.Outbox {
	t.Helper()
	ctx := context.Background()
	var ser []string
	for _, f := range []string{"seq", "cur", "dead", "deadn"} {
		ser = append(ser, string(keys.Prefix(keys.Str(f))))
	}
	op, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "wayne-manor.pebble"), FS: storage.MemFS(), Build: "batcomputer-1",
		Owners: []storage.OwnerSpec{{Owner: "core/outbox", Enabled: true, Spec: storage.Spec{Keyspace: true, Serialized: ser}}},
	})
	require.NoError(t, err)
	db, err := op.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace("core/outbox")
	require.True(t, ok)
	bus, err := events.NewBus(events.BusOptions{})
	require.NoError(t, err)
	require.NoError(t, bus.Declare("books", database.BookRelocatedTopicSpec()))
	bus.Freeze()
	ob, err := outbox.New(outbox.Options{Keyspace: ks, Catalog: bus.Catalog()})
	require.NoError(t, err)
	return ob
}

// TestRelocationConsumer_Registers: the consumer is a books task consumer of
// books.book.relocated.v1 that the outbox accepts (a declared, well-formed
// name; a durable topic of the same payload type; a books kind; exactly one of
// Live and Preview), and registration closes cleanly.
func TestRelocationConsumer_Registers(t *testing.T) {
	ob := newRelocationOutbox(t)
	require.Equal(t, "books.deluge.book-relocated", RelocationConsumerName)
	require.NoError(t, ob.DeclareConsumers("books", RelocationConsumerName))
	require.NoError(t, ob.Register("books", RelocationConsumer()))
	require.NoError(t, ob.Freeze())
	require.Equal(t, []string{RelocationConsumerName}, ob.ConsumerNames())
}

// TestRelocationConsumer_UndeclaredNameRefused: the flip must list the name in
// books' Manifest.OutboxConsumers; without it registration fails at boot.
func TestRelocationConsumer_UndeclaredNameRefused(t *testing.T) {
	ob := newRelocationOutbox(t)
	require.Error(t, ob.Register("books", RelocationConsumer()))
}

func TestRelocationRunParams(t *testing.T) {
	p, err := relocationRunParams(outbox.Message[database.BookRelocatedV1]{
		Key: "partition-key-not-the-payload", Payload: database.BookRelocatedV1{BookID: "joker-01"},
	})
	require.NoError(t, err)
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	// The run gets the book ID only: it moves the torrents to the book's path
	// as stored when it runs (the message carries no path).
	require.JSONEq(t, `{"book_id":"joker-01"}`, string(raw))
	got, err := decodeNativeParams[pathUpdateCoreParams](pathUpdateKind, raw)
	require.NoError(t, err, "the params decode as the kind's own")
	require.Equal(t, "joker-01", got.BookID)

	_, err = relocationRunParams(outbox.Message[database.BookRelocatedV1]{})
	require.ErrorIs(t, err, outbox.ErrPermanent, "a message naming no book can never start a useful run")
}

// TestPathUpdate_DedupeKeyIsTheBook: a relocation start folds only into a
// QUEUED run of the same book (Dedupe.Key); the zero policy would also fold
// into a RUNNING run with identical params, which may already have read the
// old path, and the newer move would be lost (F6-01 obligation 3).
func TestPathUpdate_DedupeKeyIsTheBook(t *testing.T) {
	c := tasks.CommonOf(byKind(t, (&Plugin{}).Kinds(fakeKindDeps(t), nil), pathUpdateKind))
	require.NotNil(t, c.Dedupe.Key)
	require.False(t, c.Dedupe.AnyParams)
	require.Nil(t, c.Dedupe.Merge)
	k, err := c.Dedupe.Key(json.RawMessage(`{"book_id":"robin-02"}`))
	require.NoError(t, err)
	require.Equal(t, "robin-02", k)
	k2, err := c.Dedupe.Key(json.RawMessage(`{"book_id":"nightwing-03"}`))
	require.NoError(t, err)
	require.NotEqual(t, k, k2)
	_, err = c.Dedupe.Key(json.RawMessage(`{"book_id":`))
	require.Error(t, err, "params the run would refuse are refused at the start")
	_, err = c.Dedupe.Key(json.RawMessage(`{"book_id":"robin-02","dry_run":true}`))
	require.Error(t, err, "the mode switch is not a native param")
}
