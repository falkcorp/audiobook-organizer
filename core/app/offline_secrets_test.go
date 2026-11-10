// file: core/app/offline_secrets_test.go
// version: 1.0.0
// guid: 6a2e9f14-c873-4b0d-9e51-2f7d8a3c6b90
// last-edited: 2026-11-10

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// secretsCmd is the offline command name of cmds.
func secretsCmd(t *testing.T, cmds []Command, name string) Command {
	t.Helper()
	for _, c := range cmds {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no offline command %q", name)
	return Command{}
}

// fixtureKeyring creates a keyring in stateDir the way F3-07's boot will
// (secrets.Open over a c/secrets/ keyspace; here a scratch in-memory store,
// since no boot opens the secret store before F3-07) and returns its keys'
// material, base64, for leak checks.
func fixtureKeyring(t *testing.T, stateDir string) []string {
	t.Helper()
	ctx := context.Background()
	const owner storage.Owner = "core/secrets"
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:   filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{{Owner: owner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true}},
		Build:  "coretest+fixture", FreeSpaceMargin: 1, FS: storage.MemFS(),
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ks, _ := db.Keyspace(owner)
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	s, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: ks, AppID: "gotham", InstallID: "batcave-0001"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var f struct {
		Keys []struct {
			Material []byte `json:"material"`
		} `json:"keys"`
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "secrets.keyring"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range f.Keys {
		out = append(out, base64.StdEncoding.EncodeToString(k.Material))
	}
	return out
}

// TestExportKeyring_OpensNoStore (05-config §2.9, SEC10-9): with the service
// holding the store, secrets export-keyring succeeds, reads only the state
// directory (the keyring and its header's install_id) and leaves the store's
// files alone, and the next boot is an ordinary one; rotate-derive and
// set-install-id are refused while the store is held. With the service
// stopped, rotate-derive adds d2 and asks for a new export.
func TestExportKeyring_OpensNoStore(t *testing.T) {
	opts := offlineOpts(t)
	keeper := offlineKeeper()
	served(t, opts, keeper)
	keyring := filepath.Join(opts.StateDir, "secrets.keyring")
	if _, err := os.Stat(keyring); !os.IsNotExist(err) {
		t.Fatalf("a boot created the keyring before F3-07 wires secrets.Open: %v", err)
	}
	material := fixtureKeyring(t, opts.StateDir)
	kr, err := os.ReadFile(keyring)
	if err != nil {
		t.Fatal(err)
	}
	cmds := OfflineCommands(opts, keeper)
	export := secretsCmd(t, cmds, "secrets export-keyring")
	rotate := secretsCmd(t, cmds, "secrets rotate-derive")
	setID := secretsCmd(t, cmds, "secrets set-install-id")

	a, err := New(t.Context(), opts, keeper)
	if err != nil {
		t.Fatal(err)
	}
	before := storeFiles(t, opts)
	out := filepath.Join(t.TempDir(), "keyring.export")
	var stdout bytes.Buffer
	if err := export.Run(t.Context(), []string{"--out", out}, &stdout); err != nil {
		_ = a.Close(context.Background())
		t.Fatalf("export-keyring while served = %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, kr) {
		t.Fatalf("the export is not the keyring file (%v)", err)
	}
	if !strings.Contains(stdout.String(), "d1, k1") || !strings.Contains(stdout.String(), "batcave-0001") {
		t.Fatalf("export output %q", stdout.String())
	}
	for _, m := range material {
		if strings.Contains(stdout.String(), m) {
			t.Fatal("export-keyring printed key material")
		}
	}
	if after := storeFiles(t, opts); !slices.Equal(before, after) {
		t.Fatalf("export-keyring changed the store's files:\nbefore %v\nafter  %v", before, after)
	}
	if _, err := os.Stat(filepath.Join(opts.StateDir, "secrets.keyring.exported")); err != nil {
		t.Fatalf("no export record: %v", err)
	}
	if err := rotate.Run(t.Context(), nil, &stdout); err == nil {
		t.Fatal("rotate-derive ran while the service held the store")
	}
	if err := setID.Run(t.Context(), []string{"--install-id", "batcave-0001"}, &stdout); err == nil {
		t.Fatal("set-install-id ran while the service held the store")
	}
	if now, _ := os.ReadFile(keyring); !bytes.Equal(now, kr) {
		t.Fatal("a refused command changed the keyring")
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	served(t, opts, keeper)

	stdout.Reset()
	if err := rotate.Run(t.Context(), nil, &stdout); err != nil {
		t.Fatalf("rotate-derive with the service stopped = %v", err)
	}
	if !strings.Contains(stdout.String(), "d2 is active") || !strings.Contains(stdout.String(), "export-keyring again") {
		t.Fatalf("rotate-derive output %q", stdout.String())
	}
	if err := setID.Run(t.Context(), []string{"--install-id", "batcave-0001"}, &stdout); err == nil {
		t.Fatal("set-install-id accepted an ID that is not the store's marker")
	}

	// Refusals: no --out, a loose destination, no storage.
	if err := export.Run(t.Context(), nil, &stdout); err == nil {
		t.Fatal("export-keyring without --out succeeded")
	}
	loose := filepath.Join(t.TempDir(), "loose")
	if err := os.WriteFile(loose, []byte("arkham"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := export.Run(t.Context(), []string{"--out", loose}, &stdout); err == nil {
		t.Fatal("export-keyring wrote over a 0644 file")
	}
	if err := setID.Run(t.Context(), nil, &stdout); err == nil {
		t.Fatal("set-install-id without --install-id succeeded")
	}
	bare := OfflineCommands(opts)
	if err := secretsCmd(t, bare, "secrets export-keyring").Run(t.Context(), []string{"--out", out}, &stdout); err == nil {
		t.Fatal("export-keyring of an app without storage succeeded")
	}
}
