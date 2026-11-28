// file: internal/plugins/deluge/relocation_follow_up_durable_test.go
// version: 0.1.0
// guid: 5b1e8d37-2c64-4a90-8e13-d07f4a96c2b5
// last-edited: 2026-11-28

package deluge

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// booksRelocationModule is the books module reduced to what the relocation
// follow-up uses: the real topic, the real consumer, and the REAL path-update
// definition (the one the books module registers, not a fixture with a Start
// the system actor can hold). Its manifest declares what the real definition's
// Start names under the books namespace, and the keyspace a staging batch needs.
type booksRelocationModule struct {
	t    *testing.T
	deps *module.Deps
	// def is the real definition; registered tells whether Register records it.
	def        tasks.Definition
	registered bool
}

func (m *booksRelocationModule) Manifest() module.Manifest {
	return module.Manifest{
		ID: "books", Version: "1.0.0", DisplayName: "Books", MinCoreVersion: "0.1.0", DefaultEnabled: true,
		Permissions:     []rbac.PermissionSpec{{Name: "books:settings.manage", Title: "Manage books settings"}},
		TaskLedger:      []tasks.Kind{pathUpdateKind},
		OutboxConsumers: []string{RelocationConsumerName},
		Storage:         storage.Spec{Keyspace: true},
	}
}

func (m *booksRelocationModule) Register(r *module.Registrar) error {
	d := r.Deps()
	m.deps = &d
	r.Resources(string(ops.ResFiles))
	r.Topics(database.BookRelocatedTopicSpec())
	if m.registered {
		r.Tasks(m.def)
	}
	r.OutboxHandler(RelocationConsumer())
	return r.Err()
}

// realPathUpdate is the real path-update definition, with the one edit core's
// kernel needs to register it. The definition's Start is the settings.manage
// expansion, which includes core:system.view, a view-class scope, and the
// kernel's SEC-9 check refuses a Start naming one unless the definition gives
// Permissions.StartableByViewers a reason. Whether and how the books module
// does is F5-18's registration decision (the deluge kinds pin it empty today,
// kinds_test.go), so this test supplies it and leaves Start and
// OutboxSystemStart exactly as the production definition has them.
func realPathUpdate(t *testing.T) tasks.Definition {
	t.Helper()
	return (&Plugin{}).pathUpdateWith(fakeKindDeps(t), nil, func(c *tasks.Common) {
		c.Perms.StartableByViewers = "test only: F5-18 decides how books registers a Start that names core:system.view"
	})
}

// stage commits one books.book.relocated.v1 message in the module's own keyspace
// batch, the way the store's copy-organize write stages it.
func (m *booksRelocationModule) stage(ctx context.Context, t *testing.T, key string, p database.BookRelocatedV1) {
	t.Helper()
	b := m.deps.Storage.Keyspace.NewBatch()
	defer func() { _ = b.Close() }()
	require.NoError(t, outbox.Stage(m.deps.Outbox, b, database.BookRelocatedTopic(), key, p))
	require.NoError(t, b.Commit(ctx, storage.Sync))
}

// TestRelocationFollowUpDurable is the start half of the relocation follow-up,
// against the REAL books.deluge.path-update definition on a real core boot: a
// committed books.book.relocated.v1 message is delivered by the relay and starts
// exactly one path-update run, in Live mode, as the books system actor.
//
// What it pins, each with a mutant that fails it:
//   - the consumer's mode is Live (#4798 m5): a Preview run moves no torrent;
//   - the real definition's Start (the settings.manage expansion, which includes
//     core:system.manage) is startable by the outbox through
//     Perms.OutboxSystemStart: without it the P7 check refuses the boot (B1 of
//     #5016), so a fixture with an easier Start cannot hide it;
//   - the start folds duplicates of one message into one run.
//
// The crash-restart half (a restart between commit and delivery) is the outbox's
// own (core/outbox crash_test.go); it does not depend on the definition.
func TestRelocationFollowUpDurable(t *testing.T) {
	m := &booksRelocationModule{t: t, def: realPathUpdate(t), registered: true}
	h := coretest.New(t, coretest.WithModules(m))
	require.NotNil(t, m.deps)

	m.stage(t.Context(), t, "joker-01", database.BookRelocatedV1{BookID: "joker-01", TargetBookID: "joker-02"})

	var runs []tasks.RunView
	require.Eventually(t, func() bool {
		runs = h.Runs(t, pathUpdateKind)
		return len(runs) > 0
	}, 30*time.Second, 50*time.Millisecond, "a committed relocation message started no path-update run: the relay, the consumer or the start rule refused it")
	time.Sleep(500 * time.Millisecond) // a duplicate start would have landed by now
	runs = h.Runs(t, pathUpdateKind)
	require.Len(t, runs, 1, "one message must start exactly one run")
	require.Equal(t, tasks.ModeLive, runs[0].Mode, "the relocation follow-up must start Live: a Preview run moves no torrent (#4798 m5)")
	rec := h.TaskRecord(t, runs[0].ID)
	require.Equal(t, tasks.Actor{AuthMethod: "system", System: "books"}, rec.Actor, "the start is the books system actor's")
	require.Equal(t, tasks.TriggerOutbox, rec.Trigger)
}

// TestRelocationFollowUpDurable_BootRefusesAKindNobodyRegistered: the consumer
// wired and its kind not registered (what the books module is until F5-18
// registers path-update) is refused at boot, naming the consumer, and never
// accepted into a relay that would dead-letter every relocation.
func TestRelocationFollowUpDurable_BootRefusesAKindNobodyRegistered(t *testing.T) {
	m := &booksRelocationModule{t: t, def: realPathUpdate(t)}
	_, err := coretest.Boot(t, coretest.WithModules(m))
	require.Error(t, err)
	require.Contains(t, err.Error(), RelocationConsumerName)
	require.Contains(t, err.Error(), string(pathUpdateKind))
}
