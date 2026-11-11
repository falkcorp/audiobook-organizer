// file: core/integrations/destination_key_test.go
// version: 1.0.0
// guid: 13da395a-9492-4e4a-abd8-21a1eb00e67e
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/integrations"
)

// R12 (SEC-15): a trust signature names the key of every destination it
// covers, so one origin under two keys, or one origin written as a URL and
// as a host-and-port pair, are different destinations. hello.gargoyle holds
// every shape: url, alt_url, and host with port.

// perch is a hello.gargoyle instance holding settings.
func perch(settings map[string]any) integrations.InstanceConfig {
	return integrations.InstanceConfig{Ref: perchRef, Adapter: gargoyle, Name: "Wayne perch", Enabled: true, Settings: settings}
}

// moveDestination rewrites perch's committed row to hold settings, keeping
// its SecretsOrigin and LocalOrigin: the row an outside writer, or an
// operator overlay that has come and gone, leaves behind.
func moveDestination(t *testing.T, e *env, settings map[string]any) {
	t.Helper()
	rewriteRow(t, e, perchRef, func(row *integrations.StoredInstance) {
		moved := maps.Clone(row.Config.Settings)
		for _, k := range []string{"url", "alt_url", "host", "port"} {
			delete(moved, k)
		}
		maps.Copy(moved, settings)
		row.Config.Settings = moved
	})
}

// requireWithheld fails t unless perch's stored password is withheld and
// was sent no more often than before, the adapter's default URL included.
func requireWithheld(t *testing.T, e *env, r *integrations.Registry, before int) {
	t.Helper()
	h, _ := r.Health(context.Background(), perchRef)
	if h.Status != integrations.HealthDegraded || h.Message != integrations.ReasonStoredSecretWithheld+": password" {
		t.Errorf("Health = %+v, want the stored password withheld", h)
	}
	if n := sentWith(e, "alfred-1"); n != before {
		t.Errorf("the stored password was sent %d more times: %v", n-before, e.out.Sent())
	}
}

// TestBind_SecretFollowsItsKey: the same origin under another key, or as a
// host-and-port pair, withholds the stored password.
func TestBind_SecretFollowsItsKey(t *testing.T) {
	const origin = "http://192.0.2.20/"
	for _, tc := range []struct {
		name  string
		moved map[string]any
		want  string // the SecretsOrigin written
	}{
		{"url to alt_url", map[string]any{"alt_url": origin}, "url=http://192.0.2.20:80"},
		{"url to host and port", map[string]any{"host": "192.0.2.20", "port": 80}, "url=http://192.0.2.20:80"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			mustPut(t, admin(), e.registry(t, nil, nil), perch(map[string]any{"url": origin}), cred("alfred-1"))
			if row, _ := e.stored(t, perchRef); row.SecretsOrigin != tc.want {
				t.Fatalf("SecretsOrigin = %q, want %q", row.SecretsOrigin, tc.want)
			}
			if h, _ := e.registry(t, nil, nil).Health(context.Background(), perchRef); h.Status != integrations.HealthOK {
				t.Fatalf("Health at the destination the password was written for = %+v", h)
			}
			moveDestination(t, e, tc.moved)
			requireWithheld(t, e, e.registry(t, nil, nil), sentWith(e, "alfred-1"))
		})
	}
}

// TestBind_SmtpURLIsNotTheHostAndPortPair: smtp://h:25 as a URL and as the
// pair host=h port=25 render one origin, and are two destinations.
func TestBind_SmtpURLIsNotTheHostAndPortPair(t *testing.T) {
	e := newEnv(t)
	mustPut(t, admin(), e.registry(t, nil, nil), perch(map[string]any{"host": "192.0.2.20", "port": 25}), cred("alfred-1"))
	if row, _ := e.stored(t, perchRef); row.SecretsOrigin != "host=smtp://192.0.2.20:25" {
		t.Fatalf("SecretsOrigin = %q", row.SecretsOrigin)
	}
	moveDestination(t, e, map[string]any{"url": "smtp://192.0.2.20:25"})
	requireWithheld(t, e, e.registry(t, nil, nil), 0)
}

// TestBind_OverlayMovesDestinationToAnotherKey is R12 as an operator meets
// it: an admin saves a password while the operator's overlay supplies the
// URL, then the operator moves the same origin to alt_url. The password
// must not follow it to the adapter's default URL.
func TestBind_OverlayMovesDestinationToAnotherKey(t *testing.T) {
	ctx := context.Background()
	const origin = "http://192.0.2.20/"
	e := newEnv(t)
	r := e.registry(t, overlay("integrations.notify.wayne-perch.url", origin), nil)
	mustPut(t, admin(), r, perch(nil), cred("alfred-1"))
	if row, _ := e.stored(t, perchRef); row.SecretsOrigin != "url=http://192.0.2.20:80" {
		t.Fatalf("SecretsOrigin = %q", row.SecretsOrigin)
	}
	if h, _ := r.Health(ctx, perchRef); h.Status != integrations.HealthOK || !slices.Equal(e.credentialsSentTo(origin), []string{"alfred-1"}) {
		t.Fatalf("Health at the overlay's URL = %+v", h)
	}
	before := sentWith(e, "alfred-1")
	if err := r.SetOverlay(ctx, overlay("integrations.notify.wayne-perch.alt_url", origin)); err != nil {
		t.Fatal(err)
	}
	requireWithheld(t, e, r, before)
}

// TestBind_LocalFollowsItsKey: Local granted for url is not held once the
// same origin sits under alt_url, or as a host-and-port pair.
func TestBind_LocalFollowsItsKey(t *testing.T) {
	const origin = "http://192.0.2.20/"
	for _, tc := range []struct {
		name  string
		moved map[string]any
	}{
		{"url to alt_url", map[string]any{"alt_url": origin}},
		{"url to host and port", map[string]any{"host": "192.0.2.20", "port": 80}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			in := perch(map[string]any{"url": origin})
			in.Local = true
			mustPut(t, admin(), e.registry(t, nil, nil), in, nil)
			r := e.registry(t, nil, nil)
			_, release := lease(t, r, perchRef)
			release()
			if tr := lastSpec(t, e, perchRef).Trust; !slices.Equal(tr.PrivateOrigins, []string{"http://192.0.2.20:80"}) {
				t.Fatalf("Local trust at the granted destination = %v", tr)
			}
			if row, _ := e.stored(t, perchRef); row.LocalOrigin != "url=http://192.0.2.20:80" {
				t.Fatalf("LocalOrigin = %q", row.LocalOrigin)
			}
			moveDestination(t, e, tc.moved)
			r = e.registry(t, nil, nil)
			_, release = lease(t, r, perchRef)
			release()
			if tr := lastSpec(t, e, perchRef).Trust; len(tr.PrivateOrigins) != 0 {
				t.Errorf("Local followed the origin to another key: %v", tr)
			}
		})
	}
}

// TestPut_AuditNamesDestinationKeys: a destination change is audited with
// the signatures of the destinations before and after, keys included, so a
// move between keys at one origin reads as the move it is.
func TestPut_AuditNamesDestinationKeys(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, manager(), r, perch(map[string]any{"url": "http://192.0.2.20/"}), nil)
	moved := current(t, r, perchRef)
	moved.Settings = map[string]any{"alt_url": "http://192.0.2.20/"}
	mustPut(t, manager(), r, moved, nil)
	var details map[string]any
	for rec, err := range e.audit.Query(context.Background(), audit.Filter{Limit: 1}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Action != "core.integration.change_url" {
			t.Fatalf("audit action = %q", rec.Action)
		}
		details = rec.Details
	}
	if details["destinations"] != "alt_url=http://192.0.2.20:80" || details["previous_destinations"] != "url=http://192.0.2.20:80" {
		t.Errorf("audit details = %v", details)
	}
}

// TestPut_ResupplyAfterKeyMove: a password supplied beside withheld ones is
// recorded for today's destinations, keys included. A key-blind match
// between the stored SecretsOrigin (url=...) and the stored destination
// (alt_url=...) would keep the old record and withhold the new password.
func TestPut_ResupplyAfterKeyMove(t *testing.T) {
	ctx := context.Background()
	const origin = "http://192.0.2.20/"
	e := newEnv(t)
	mustPut(t, admin(), e.registry(t, nil, nil), perch(map[string]any{"url": origin}), cred("alfred-1"))
	moveDestination(t, e, map[string]any{"alt_url": origin})
	r := e.registry(t, nil, nil)
	mustPut(t, manager(), r, current(t, r, perchRef), cred("alfred-2"))
	if row, _ := e.stored(t, perchRef); row.SecretsOrigin != "alt_url=http://192.0.2.20:80" {
		t.Errorf("SecretsOrigin = %q, want the destination the new password was written for", row.SecretsOrigin)
	}
	if h, _ := r.Health(ctx, perchRef); h.Status != integrations.HealthOK || sentWith(e, "alfred-2") != 1 || sentWith(e, "alfred-1") != 0 {
		t.Errorf("Health = %+v, sent %v", h, e.out.Sent())
	}
}
