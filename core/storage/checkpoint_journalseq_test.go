// file: core/storage/checkpoint_journalseq_test.go
// version: 0.1.0
// guid: 5f0d7c3e-9b1a-4e62-8d47-2a6c1e9f3b58
// last-edited: 2026-11-21

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// backupOwnerSpec is the revocation journal's keyspace, as the kernel registers
// it from the identity flip.
func backupOwnerSpec() OwnerSpec {
	return OwnerSpec{Owner: backupOwner, Enabled: true, Spec: Spec{Keyspace: true}}
}

// readJournalHead is the hook the kernel binds (journal.ReadHead), in the test's
// own words: c/backup/journal_head is {"seq": N}.
func readJournalHead(ctx context.Context, ks Keyspace) (uint64, error) {
	raw, err := ks.Get(ctx, []byte("journal_head"))
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var h struct {
		Seq uint64 `json:"seq"`
	}
	return h.Seq, json.Unmarshal(raw, &h)
}

// seedJournalHead commits c/backup/journal_head = {"seq": seq} through an open
// store, as a revocation's batch does.
func seedJournalHead(t *testing.T, db *DB, seq uint64) {
	t.Helper()
	ks, ok := db.Keyspace(backupOwner)
	if !ok {
		t.Fatal("the store has no core/backup keyspace")
	}
	raw, err := json.Marshal(map[string]uint64{"seq": seq})
	if err != nil {
		t.Fatal(err)
	}
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	if err := b.In(ks).Set([]byte("journal_head"), raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(context.Background(), Sync); err != nil {
		t.Fatal(err)
	}
}

// upgradeAfterAHead boots a store whose journal head is seq and then boots the
// build that adds a migration step, which takes a checkpoint; it returns the
// record of that checkpoint.
func upgradeAfterAHead(t *testing.T, seq uint64, withBackup bool, hook func(context.Context, Keyspace) (uint64, error)) (checkpointRecord, error) {
	t.Helper()
	extra := func(o []OwnerSpec) []OwnerSpec {
		if withBackup {
			return append(o, backupOwnerSpec())
		}
		return o
	}
	e := headStore(t, vfs.NewMem(), extra(cpOwners())...)
	db := e.open()
	if withBackup {
		seedJournalHead(t, db, seq)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = extra(fixedOwners())
	e.opts.JournalHead = hook
	db2, err := e.tryOpen()
	if err != nil {
		return checkpointRecord{}, err
	}
	name := db2.CheckpointName()
	if name == "" {
		t.Fatal("the upgrade boot took no checkpoint")
	}
	return readRecord(t, e, name), nil
}

// TestCheckpointRecordsTheJournalHead (F4-08 B10k, 05-storage §5S.3.12 "How",
// 05-backup §5B.3.6 "Coverage at restore"): the pre-migration checkpoint's record
// carries the revocation journal's committed head seq, the boundary a restore of
// it re-applies the journal after.
func TestCheckpointRecordsTheJournalHead(t *testing.T) {
	rec, err := upgradeAfterAHead(t, 12, true, readJournalHead)
	if err != nil {
		t.Fatal(err)
	}
	if rec.JournalSeq != 12 {
		t.Fatalf("checkpoint record journal_seq = %d, want the store's journal head 12", rec.JournalSeq)
	}
}

// TestCheckpointJournalSeqIsZeroWithoutAHead: no hook, or no core/backup keyspace in
// the build, records 0 (no boundary: the safe side), and the hook is not called
// when there is no keyspace to read.
func TestCheckpointJournalSeqIsZeroWithoutAHead(t *testing.T) {
	t.Run("no hook", func(t *testing.T) {
		rec, err := upgradeAfterAHead(t, 12, true, nil)
		if err != nil || rec.JournalSeq != 0 {
			t.Fatalf("record journal_seq = %d, err %v, want 0", rec.JournalSeq, err)
		}
	})
	t.Run("no backup keyspace", func(t *testing.T) {
		called := false
		rec, err := upgradeAfterAHead(t, 12, false, func(context.Context, Keyspace) (uint64, error) {
			called = true
			return 99, nil
		})
		if err != nil || rec.JournalSeq != 0 || called {
			t.Fatalf("record journal_seq = %d, hook called %v, err %v, want 0, not called", rec.JournalSeq, called, err)
		}
	})
}

// TestCheckpointRefusesWhenTheJournalHeadCannotBeRead: a head that cannot be read
// is not recorded as 0; the boot fails before it copies anything.
func TestCheckpointRefusesWhenTheJournalHeadCannotBeRead(t *testing.T) {
	boom := errors.New("scarecrow garbled the head")
	_, err := upgradeAfterAHead(t, 12, true, func(context.Context, Keyspace) (uint64, error) { return 0, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("boot err = %v, want the hook's error", err)
	}
}
