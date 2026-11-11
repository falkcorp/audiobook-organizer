// file: core/integrations/importer_test.go
// version: 1.0.0
// guid: cabcf86d-8cd0-4e4c-9a5e-2d3f9d51add8
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// gcpdImport is the record an app's cutover step hands the importer.
func gcpdImport() integrations.StoredInstance {
	return integrations.StoredInstance{
		Config: integrations.InstanceConfig{
			Ref: signalRef, Adapter: batsignal, Name: "GCPD roof", Enabled: true, Local: true,
			Origin: integrations.OriginUI, Version: 9,
			Settings: map[string]any{
				"url": "http://192.0.2.20/", "channel": "gcpd",
				"password": secrets.RefTo("integrations.notify.gcpd-roof.password"),
			},
		},
		LocalOrigin:          "http://192.0.2.20:80",
		SecretsOrigin:        "http://192.0.2.20:80",
		OperatorSecretOrigin: map[string]string{"url": "http://192.0.2.20:80"},
	}
}

// TestImporter_ValidatesAndStagesInCallerBatch (I6, M1.10): the importer
// stages into the caller's batch only, with Origin migration and the trust
// fields it is given, audits each row, is a no-op on a re-run, and keeps a
// record the Registry can only report as misconfigured.
func TestImporter_ValidatesAndStagesInCallerBatch(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	im := integrations.NativeImporter(e.ks, integrations.ImportAudit(e.rec))
	importIn := func(inst integrations.StoredInstance) (int, error) {
		t.Helper()
		b := e.ks.NewBatch()
		defer func() {
			if err := b.Close(); err != nil {
				t.Error(err)
			}
		}()
		if err := im.ImportInstance(ctx, b, inst); err != nil {
			return b.Count(), err
		}
		n := b.Count()
		if _, ok := e.stored(t, inst.Config.Ref); ok && n > 0 {
			t.Errorf("%s is visible before the caller's commit", inst.Config.Ref)
		}
		return n, b.Commit(ctx, storage.Sync)
	}

	if _, err := importIn(gcpdImport()); err != nil {
		t.Fatalf("import: %v", err)
	}
	row, ok := e.stored(t, signalRef)
	if !ok {
		t.Fatal("the imported row is not stored")
	}
	if c := row.Config; c.Origin != integrations.OriginMigration || c.Version != 1 || !c.Local || c.Name != "GCPD roof" {
		t.Errorf("imported row = %+v", c)
	}
	if row.LocalOrigin != "http://192.0.2.20:80" || row.OperatorSecretOrigin["url"] != "http://192.0.2.20:80" {
		t.Errorf("the trust fields were not kept: %+v", row)
	}
	if want := []string{"core.integration.create", "core.integration.local_migrated"}; !slices.Equal(e.actions(t), want) {
		t.Errorf("audit = %v, want %v", e.actions(t), want)
	}
	if n, err := importIn(gcpdImport()); err != nil || n != 0 {
		t.Errorf("a re-run: %d writes, %v", n, err)
	}
	if len(e.actions(t)) != 2 {
		t.Errorf("a re-run audited again: %v", e.actions(t))
	}
	other := gcpdImport()
	other.Config.Name = "Arkham"
	if _, err := importIn(other); !errors.Is(err, storage.ErrImportConflict) {
		t.Errorf("a conflicting import: err = %v, want ErrImportConflict", err)
	}

	bad := func(edit func(*integrations.StoredInstance)) integrations.StoredInstance {
		inst := gcpdImport()
		inst.Config.Ref.Instance = "arkham"
		inst.Config.Settings = map[string]any{"url": "http://192.0.2.21/"}
		inst.OperatorSecretOrigin = maps.Clone(inst.OperatorSecretOrigin)
		edit(&inst)
		return inst
	}
	for name, inst := range map[string]integrations.StoredInstance{
		"foreign secret": bad(func(i *integrations.StoredInstance) {
			i.Config.Settings["password"] = secrets.RefTo("integrations.notify.gcpd-roof.password")
		}),
		"foreign decoded": bad(func(i *integrations.StoredInstance) {
			i.Config.Settings["password"] = map[string]any{"secret": "core.oidc.secret"}
		}),
		"pointer ref":        bad(func(i *integrations.StoredInstance) { r := secrets.RefTo("x"); i.Config.Settings["password"] = &r }),
		"secret value":       bad(func(i *integrations.StoredInstance) { i.Config.Settings["password"] = secrets.NewValue("alfred-1") }),
		"local, no origin":   bad(func(i *integrations.StoredInstance) { i.LocalOrigin = "" }),
		"malformed origin":   bad(func(i *integrations.StoredInstance) { i.OperatorSecretOrigin["url"] = "the narrows" }),
		"invalid ref":        bad(func(i *integrations.StoredInstance) { i.Config.Ref.Instance = "Arkham" }),
		"invalid adapter ID": bad(func(i *integrations.StoredInstance) { i.Config.Adapter = "lazarus" }),
	} {
		if n, err := importIn(inst); !errors.Is(err, integrations.ErrInvalid) || n != 0 {
			t.Errorf("%s: %d writes, err = %v, want ErrInvalid and nothing staged", name, n, err)
		}
	}
	if err := im.ImportInstance(ctx, nil, gcpdImport()); err == nil {
		t.Error("an import without the caller's batch succeeded")
	}
	b := e.ks.NewBatch()
	defer func() { _ = b.Close() }()
	if err := integrations.NativeImporter(e.ks).ImportInstance(ctx, b, gcpdImport()); err == nil {
		t.Error("an import without an audit store succeeded")
	}

	// Kept and reported: a record of an adapter this binary lacks and one
	// whose settings fail the schema are imported; the Registry serves them
	// as misconfigured.
	lazarus := bad(func(i *integrations.StoredInstance) {
		i.Config.Ref.Instance = "lazarus-pit"
		i.Config.Adapter = "hello.lazarus"
		i.Config.Local, i.LocalOrigin = false, "http://192.0.2.99:80"
	})
	riddle := bad(func(i *integrations.StoredInstance) {
		i.Config.Ref.Instance = "riddler"
		i.Config.Settings["channel"] = 7
	})
	for _, inst := range []integrations.StoredInstance{lazarus, riddle} {
		if _, err := importIn(inst); err != nil {
			t.Fatalf("import %s: %v", inst.Config.Ref, err)
		}
	}
	if row, _ := e.stored(t, lazarus.Config.Ref); row.LocalOrigin != "" {
		t.Errorf("a LocalOrigin without Local was kept: %q", row.LocalOrigin)
	}
	r := e.registry(t, nil, nil)
	for ref, want := range map[integrations.Ref]string{
		lazarus.Config.Ref: integrations.ReasonAdapterNotInBinary,
		riddle.Config.Ref:  integrations.ReasonInvalidSettings,
	} {
		if h, err := r.Health(ctx, ref); err != nil || h.Status != integrations.HealthMisconfigured || h.Message != want {
			t.Errorf("%s: Health = %+v, %v; want misconfigured %q", ref, h, err, want)
		}
	}
}
