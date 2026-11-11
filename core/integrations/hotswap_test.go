// file: core/integrations/hotswap_test.go
// version: 1.0.0
// guid: ce5d36d1-0d86-4c66-bd21-8f83551eda8e
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The stand-ins of a Registry that never writes: the hot swap needs no
// storage, so the bubble holds no store goroutine.
type (
	memStore  struct{ rows []integrations.StoredInstance }
	noSecrets struct{}
	noAudit   struct{}
)

var errReadOnly = errors.New("read-only stand-in")

func (m memStore) Load(context.Context) ([]integrations.StoredInstance, error) { return m.rows, nil }
func (memStore) Put(context.Context, *storage.Batch, integrations.StoredInstance) (uint64, error) {
	return 0, errReadOnly
}
func (memStore) Delete(context.Context, *storage.Batch, integrations.Ref) error { return errReadOnly }

func (noSecrets) Get(context.Context, secrets.Name) (secrets.Value, secrets.Meta, error) {
	return secrets.Value{}, secrets.Meta{}, secrets.ErrNotSet
}
func (noSecrets) Meta(context.Context, secrets.Name) (secrets.Meta, error) {
	return secrets.Meta{}, nil
}
func (noSecrets) Stage(context.Context, *storage.Batch, secrets.Write) error { return errReadOnly }

func (noAudit) RecordIn(context.Context, *storage.Batch, audit.Event) error { return errReadOnly }

// TestRegistry_HotSwapDrainsLeases (M1.4 step 4, M1.6): a swapped-out
// instance is Closed only after its last lease is released, or once
// core.integrations.drain_timeout passes; a new lease gets the new instance;
// Close drains the rest.
func TestRegistry_HotSwapDrainsLeases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		fake := &integrationstest.Fake{}
		tower := integrations.Ref{Kind: integrations.KindNotify, Instance: "wayne-tower"}
		r, err := integrations.New(integrations.Options{
			Store: memStore{}, NewBatch: func() *storage.Batch { return nil }, Secrets: noSecrets{}, Audit: noAudit{},
			Outbound:          (&integrationstest.Outbound{}).Func(),
			OperatorInstances: map[integrations.Ref]integrations.AdapterID{tower: batsignal},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Register("hello", fake.Descriptor(integrations.Meta{ID: batsignal, Kind: integrations.KindNotify, Settings: schema[batsignalSettings](t)})); err != nil {
			t.Fatal(err)
		}
		at := func(url string) error {
			return r.SetOverlay(ctx, overlay("integrations.notify.wayne-tower.url", url))
		}
		if err := r.Load(ctx, overlay("integrations.notify.wayne-tower.url", "http://192.0.2.30/")); err != nil {
			t.Fatal(err)
		}

		first, releaseFirst := lease(t, r, tower)
		if err := at("http://192.0.2.31/"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if first.Closed() != 0 {
			t.Fatal("a swapped-out instance was closed while a lease was held")
		}
		second, releaseSecond := lease(t, r, tower)
		if second == first || second.Config.Settings["url"] != "http://192.0.2.31/" {
			t.Fatalf("a lease after the swap got the old instance")
		}
		releaseFirst()
		releaseFirst()
		synctest.Wait()
		if first.Closed() != 1 {
			t.Fatalf("the drained instance was closed %d times, want 1", first.Closed())
		}

		if err := at("http://192.0.2.32/"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(29 * time.Second)
		synctest.Wait()
		if second.Closed() != 0 {
			t.Fatal("an instance was closed before the drain timeout with a lease held")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if second.Closed() != 1 {
			t.Fatal("the drain timeout did not close an instance whose lease was never released")
		}
		releaseSecond()

		third, releaseThird := lease(t, r, tower)
		short, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := r.Close(short); err == nil {
			t.Error("Close returned while a lease was still held")
		}
		if _, _, err := integrations.Lease[integrations.Instance](ctx, r, tower); !errors.Is(err, integrations.ErrClosed) {
			t.Errorf("Lease after Close: %v", err)
		}
		releaseThird()
		if err := r.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if third.Closed() != 1 {
			t.Errorf("Close did not close the last instance")
		}
	})
}
