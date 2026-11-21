// file: internal/books/cutover/m6/credentials_registry_test.go
// version: 1.0.0
// guid: 7a2d5c19-e40b-4f83-b1d6-93c0e8a5f726

package m6_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/adapters/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The books metadata adapters' sources, by instance ID.
var metadataIDs = []string{"audible", "audnexus", "google-books", "hardcover", "openlibrary", "wikipedia"}

func metadataRef(id string) integrations.Ref {
	return integrations.Ref{Kind: integrations.KindMetadata, Instance: integrations.InstanceID(id)}
}

// liveSecrets serves the rig's store to a Registry: a secret M6 or the repair
// wrote, or one M2 left.
type liveSecrets struct{ r *rig }

func (l liveSecrets) Get(ctx context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	v, ok, err := l.r.secrets.value(ctx, n)
	switch {
	case err != nil:
		return secrets.Value{}, secrets.Meta{}, err
	case !ok:
		return secrets.Value{}, secrets.Meta{}, secrets.ErrNotSet
	}
	return secrets.NewValue(v), secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored}, nil
}

func (l liveSecrets) Meta(ctx context.Context, n secrets.Name) (secrets.Meta, error) {
	_, m, err := l.Get(ctx, n)
	return m, err
}

func (liveSecrets) Stage(context.Context, *storage.Batch, secrets.Write) error {
	return errors.New("unused")
}

type quietAudit struct{}

func (quietAudit) RecordIn(context.Context, *storage.Batch, audit.Event) error { return nil }

// loadRegistry is the books metadata adapters registered in a real Registry over
// the store M6 wrote, and loaded: what the metadata flip's boot does. No
// Outbound is set, so no request can leave.
func loadRegistry(ctx context.Context, t *testing.T, r *rig, db *storage.DB) *integrations.Registry {
	t.Helper()
	ks, ok := db.Keyspace(ownerInteg)
	if !ok {
		t.Fatal("no integrations keyspace")
	}
	reg, err := integrations.New(integrations.Options{
		Store: integrations.NativeInstanceStore(ks), NewBatch: ks.NewBatch,
		Secrets: liveSecrets{r}, Audit: quietAudit{},
	})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	ds, err := metadata.Descriptors(metadata.Options{})
	if err != nil {
		t.Fatalf("descriptors: %v", err)
	}
	if err := reg.Register("books", ds...); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Load(ctx, nil); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(context.Background()) })
	return reg
}

// healths is the Registry's health of the six metadata sources.
func healths(ctx context.Context, t *testing.T, reg *integrations.Registry) map[string]integrations.Health {
	t.Helper()
	out := map[string]integrations.Health{}
	for _, id := range metadataIDs {
		h, err := reg.Health(ctx, metadataRef(id))
		if err != nil {
			t.Fatalf("health of %s: %v", id, err)
		}
		out[id] = h
	}
	return out
}

// misconfigured is the ids the Registry reports with an undeclared setting: the
// state that serves no request and sends none.
func misconfigured(hs map[string]integrations.Health) []string {
	var out []string
	for id, h := range hs {
		if h.Status == integrations.HealthMisconfigured && h.Message == integrations.ReasonInvalidSettings {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// withLive migrates the fixture, then runs fn over the live store.
func withLive(t *testing.T, opts []func(*rig), fn func(ctx context.Context, r *rig, db *storage.DB, s *snap)) {
	t.Helper()
	r := newRig(t, append([]func(*rig){withRows(headRows(t))}, opts...)...)
	fsys := r.seed(t)
	dm := r.migration()
	err := r.boot(context.Background(), fsys, dm, func(ctx context.Context, db *storage.DB) error {
		s, err := r.read(ctx, t, db, dm)
		if err != nil {
			return err
		}
		if s.State != "done" {
			t.Fatalf("state %q, want done", s.State)
		}
		fn(ctx, r, db, s)
		return nil
	})
	if err != nil {
		t.Fatalf("M6: %v\nlogs:\n%s", err, r.logs.String())
	}
}

// allCredentials seeds a credentials entry for every source, the shape of a HEAD
// config whose sources each held an apiKey: hardcover has the operator's token
// as well (which wins), google-books has no operator key (so its credential is
// the key), the other four have a credential no adapter reads.
func allCredentials() []func(*rig) {
	add := map[secrets.Name]string{
		credName("hardcover", "api_key"):    "fixture-cred-key-bane",
		credName("google-books", "api_key"): "fixture-credential-key-riddler",
	}
	for _, id := range []string{"audible", "audnexus", "wikipedia"} {
		add[credName(id, "api_key")] = "fixture-" + id + "-key-gordon"
	}
	return []func(*rig){withExtraSources, withSecrets(add, fieldName("google-books", "api_key"))}
}

// TestMetadataCredentials_LoadThroughARegistry: the store M6 writes, loaded by a
// real Registry with the six books adapters, serves every source: none is
// HealthMisconfigured for an undeclared setting, and the folded credential
// resolves as the declared field's secret. Without the fold every one of the six
// would be misconfigured and dark, which TestRepair... below shows on the
// pre-fold shape.
func TestMetadataCredentials_LoadThroughARegistry(t *testing.T) {
	withLive(t, allCredentials(), func(ctx context.Context, r *rig, db *storage.DB, s *snap) {
		reg := loadRegistry(ctx, t, r, db)
		hs := healths(ctx, t, reg)
		if bad := misconfigured(hs); len(bad) != 0 {
			t.Errorf("sources the Registry reports with invalid settings: %v", bad)
		}
		for id, h := range hs {
			if h.Status == integrations.HealthMisconfigured {
				t.Errorf("%s is misconfigured: %q", id, h.Message)
			}
		}
		gb, ok := reg.Config(metadataRef("google-books"))
		if !ok {
			t.Fatal("google-books is not served")
		}
		ref, isRef := gb.Settings["api_key"].(secrets.Ref)
		if !isRef || ref.Name() != fieldName("google-books", "api_key") {
			t.Fatalf("google-books api_key is %v, want a ref to %s", gb.Settings["api_key"], fieldName("google-books", "api_key"))
		}
		v, err := ref.Bind(liveSecrets{r}).Resolve(ctx)
		if err != nil || v.Reveal() != "fixture-credential-key-riddler" {
			t.Errorf("the folded key resolves to %q (%v), want the credential's value", v.Reveal(), err)
		}
		for _, id := range metadataIDs {
			c, ok := reg.Config(metadataRef(id))
			if !ok {
				t.Errorf("%s is not served", id)
				continue
			}
			for k := range c.Settings {
				if strings.Contains(k, ".") {
					t.Errorf("%s holds the dotted setting %q", id, k)
				}
			}
		}
	})
}

// oldShape rewrites the migrated rows of three sources into what an M6 that
// predates the fold wrote: every secret under the instance's prefix bound as a
// setting of its own dotted name.
func oldShape(ctx context.Context, t *testing.T, ks storage.Keyspace, bind map[string]map[string]secrets.Name) {
	t.Helper()
	st := integrations.NativeInstanceStore(ks)
	rows, err := st.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		set, ok := bind[string(row.Config.Ref.Instance)]
		if !ok || row.Config.Ref.Kind != integrations.KindMetadata {
			continue
		}
		row.Config.Settings = map[string]any{}
		for field, n := range set {
			row.Config.Settings[field] = secrets.RefTo(n)
		}
		if row.SecretsOrigin == "" {
			row.SecretsOrigin = "adapter-default"
		}
		b := ks.NewBatch()
		if _, err := st.Put(ctx, b, row); err != nil {
			t.Fatalf("rewrite %s: %v", row.Config.Ref, err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
	}
}

// forgetSecret deletes a secret row M6's own fold wrote, so a store is as an M6
// without the fold left it: the instance holds the dotted setting and nothing
// stands at the declared name.
func forgetSecret(ctx context.Context, t *testing.T, r *rig, db *storage.DB, n secrets.Name) {
	t.Helper()
	ks, _ := db.Keyspace(ownerSecrets)
	b := ks.NewBatch()
	// A Serialized range refuses a write that carries no Expect on its key.
	b.Bind(ks)
	if _, err := b.GetForUpdate(ctx, ks, []byte(n)); err != nil {
		t.Fatal(err)
	}
	if err := b.In(ks).Delete([]byte(n)); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if v, found, err := r.secrets.value(ctx, n); err != nil || found {
		t.Fatalf("%s still holds %q after it was forgotten (found %v, %v)", n, v, found, err)
	}
}

func repairConfig(r *rig, ks storage.Keyspace, rep *m6.Report) m6.RepairConfig {
	return m6.RepairConfig{
		Store: integrations.NativeInstanceStore(ks), Secrets: r.secrets, NewBatch: ks.NewBatch,
		SecretValue: r.secrets.value, Audit: r.audit, Report: rep, By: "m6 repair test",
	}
}

// TestRepairMetadataCredentials: a store an M6 without the fold already ran on
// (create-only: running M6 again cannot fix it) is repaired in place. Before, the
// Registry reports the three rewritten sources HealthMisconfigured with an
// undeclared setting; after, none, the folded credential is a secret under the
// declared name, nothing is deleted, one audit record names each repaired row,
// and a second run finds nothing to do.
func TestRepairMetadataCredentials(t *testing.T) {
	gbCred, hcCred := credName("google-books", "api_key"), credName("hardcover", "api_key")
	opts := []func(*rig){withExtraSources, withSecrets(map[secrets.Name]string{
		gbCred: "fixture-credential-key-riddler",
		hcCred: "fixture-cred-key-bane",
	}, fieldName("google-books", "api_key"))}
	withLive(t, opts, func(ctx context.Context, r *rig, db *storage.DB, s *snap) {
		ks, _ := db.Keyspace(ownerInteg)
		oldShape(ctx, t, ks, map[string]map[string]secrets.Name{
			"google-books": {"credentials.api_key": gbCred},
			"hardcover":    {"api_token": fieldName("hardcover", "api_token"), "credentials.api_key": hcCred},
			"openlibrary":  {"credentials.token": credName("openlibrary", "token")},
		})
		forgetSecret(ctx, t, r, db, fieldName("google-books", "api_key"))
		before := healths(ctx, t, loadRegistry(ctx, t, r, db))
		if got, want := misconfigured(before), []string{"google-books", "hardcover", "openlibrary"}; !slices.Equal(got, want) {
			t.Fatalf("before the repair the misconfigured sources are %v, want %v (the pre-fold shape is not what this test thinks)", got, want)
		}
		rep := &m6.Report{}
		res, err := m6.RepairMetadataCredentials(ctx, repairConfig(r, ks, rep))
		if err != nil {
			t.Fatal(err)
		}
		// Three rewritten, the other three sources need nothing, and every row of
		// the other kinds (the fixture's ai, download and notify instances) is skipped.
		if want := (m6.RepairResult{Repaired: 3, Unchanged: 3, Skipped: len(wantInstances) - 3}); res != want {
			t.Errorf("result %+v, want %+v", res, want)
		}
		after := healths(ctx, t, loadRegistry(ctx, t, r, db))
		if bad := misconfigured(after); len(bad) != 0 {
			t.Errorf("after the repair the misconfigured sources are %v", bad)
		}
		rows, err := integrations.NativeInstanceStore(ks).Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]map[string]string{}
		for _, row := range rows {
			if row.Config.Ref.Kind != integrations.KindMetadata {
				continue
			}
			m := map[string]string{}
			for k, v := range row.Config.Settings {
				if ref, ok := v.(secrets.Ref); ok {
					m[k] = string(ref.Name())
				}
			}
			got[string(row.Config.Ref.Instance)] = m
		}
		want := map[string]map[string]string{
			"google-books": {"api_key": string(fieldName("google-books", "api_key"))},
			"hardcover":    {"api_token": string(fieldName("hardcover", "api_token"))},
			"openlibrary":  {},
		}
		for id, w := range want {
			if !maps.Equal(got[id], w) {
				t.Errorf("%s secret settings %v, want %v", id, got[id], w)
			}
		}
		// The credential is a secret under the declared name now; the originals stay.
		if v, ok, _ := r.secrets.value(ctx, fieldName("google-books", "api_key")); !ok || v != "fixture-credential-key-riddler" {
			t.Errorf("the declared name holds %q (found %v)", v, ok)
		}
		for _, n := range []secrets.Name{gbCred, hcCred, credName("openlibrary", "token")} {
			if _, ok, _ := r.secrets.value(ctx, n); !ok {
				t.Errorf("the repair deleted the secret %s", n)
			}
		}
		for _, id := range []string{"google-books", "hardcover", "openlibrary"} {
			if _, ok := auditRow(ctx, t, db, "core.integration.update|metadata/"+id); !ok {
				t.Errorf("no update audit record for metadata/%s", id)
			}
		}
		for _, e := range rep.Entries() {
			for _, v := range r.secrets.seed {
				if strings.Contains(e.Detail, v) {
					t.Errorf("the repair report holds a secret value: %v", e)
				}
			}
		}
		again, err := m6.RepairMetadataCredentials(ctx, repairConfig(r, ks, rep))
		if err != nil {
			t.Fatal(err)
		}
		if want := (m6.RepairResult{Unchanged: 6, Skipped: len(wantInstances) - 3}); again != want {
			t.Errorf("a second repair %+v, want %+v", again, want)
		}
	})
}

// TestRepairMetadataCredentials_LeavesOtherRowsAlone: a row an operator wrote
// (origin api) or another kind's row is never touched, even holding the same
// dotted setting, and an ID with no adapter in this binary is skipped.
func TestRepairMetadataCredentials_LeavesOtherRowsAlone(t *testing.T) {
	withLive(t, []func(*rig){withSecrets(map[secrets.Name]string{credName("hardcover", "api_key"): "fixture-cred-key-bane"})}, func(ctx context.Context, r *rig, db *storage.DB, s *snap) {
		ks, _ := db.Keyspace(ownerInteg)
		st := integrations.NativeInstanceStore(ks)
		rows, err := st.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Config.Ref != metadataRef("hardcover") {
				continue
			}
			row.Config.Origin = integrations.OriginAPI
			row.Config.Settings = map[string]any{"credentials.api_key": secrets.RefTo(credName("hardcover", "api_key"))}
			b := ks.NewBatch()
			if _, err := st.Put(ctx, b, row); err != nil {
				t.Fatal(err)
			}
			if err := b.Commit(ctx, storage.Sync); err != nil {
				t.Fatal(err)
			}
		}
		res, err := m6.RepairMetadataCredentials(ctx, repairConfig(r, ks, nil))
		if err != nil {
			t.Fatal(err)
		}
		if res.Repaired != 0 {
			t.Errorf("repaired %d rows, want 0: the only dotted row is an operator's", res.Repaired)
		}
		after, err := st.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range after {
			if row.Config.Ref == metadataRef("hardcover") {
				if _, ok := row.Config.Settings["credentials.api_key"]; !ok {
					t.Error("an operator's row was rewritten")
				}
			}
		}
	})
}

// TestRepairMetadataCredentials_RefusesAnIncompleteConfig: a RepairConfig missing
// a piece is an error, never a silent no-op.
func TestRepairMetadataCredentials_RefusesAnIncompleteConfig(t *testing.T) {
	if _, err := m6.RepairMetadataCredentials(context.Background(), m6.RepairConfig{}); err == nil {
		t.Error("an empty RepairConfig was accepted")
	}
}

// auditRow reads one record of core's audit keyspace.
func auditRow(ctx context.Context, t *testing.T, db *storage.DB, key string) ([]byte, bool) {
	t.Helper()
	ks, ok := db.Keyspace(ownerAudit)
	if !ok {
		t.Fatal("no audit keyspace")
	}
	raw, err := ks.Get(ctx, []byte(key))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, false
	case err != nil:
		t.Fatal(err)
	}
	return raw, true
}

// TestRepairMetadataCredentials_UnreadableCredential: a credential M2 could not
// read is folded as an unreadable marker at the declared name (there are no bytes
// to copy), exactly as M6's own fold does, and the secret setting is bound to it.
func TestRepairMetadataCredentials_UnreadableCredential(t *testing.T) {
	gbCred := credName("google-books", "api_key")
	declared := fieldName("google-books", "api_key")
	opts := []func(*rig){withSecrets(map[secrets.Name]string{}, declared, gbCred), func(r *rig) { r.secrets.unreadable[gbCred] = true }}
	withLive(t, opts, func(ctx context.Context, r *rig, db *storage.DB, s *snap) {
		ks, _ := db.Keyspace(ownerInteg)
		oldShape(ctx, t, ks, map[string]map[string]secrets.Name{"google-books": {"credentials.api_key": gbCred}})
		forgetSecret(ctx, t, r, db, declared)
		res, err := m6.RepairMetadataCredentials(ctx, repairConfig(r, ks, nil))
		if err != nil {
			t.Fatal(err)
		}
		if res.Repaired != 1 {
			t.Fatalf("repaired %d rows, want 1", res.Repaired)
		}
		sks, _ := db.Keyspace(ownerSecrets)
		raw, err := sks.Get(ctx, []byte(declared))
		if err != nil || string(raw) != unreadableMarker+"corrupt" {
			t.Errorf("the declared name holds %q (%v), want the corrupt unreadable marker", raw, err)
		}
		rows, err := integrations.NativeInstanceStore(ks).Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Config.Ref == metadataRef("google-books") {
				if ref, ok := row.Config.Settings["api_key"].(secrets.Ref); !ok || ref.Name() != declared {
					t.Errorf("api_key is %v, want a ref to %s", row.Config.Settings["api_key"], declared)
				}
			}
		}
	})
}
