// file: core/kernel/cutover_importers_native_test.go
// version: 0.1.0
// guid: c58d27e1-9b03-4a64-8e3f-4d71a0b6f259
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// readableAuth and readableRoles are importers that can also read back, as the
// identity boot's real stores do.
type readableAuth struct{ auth.Importer }

func (readableAuth) GetUser(_ context.Context, id auth.UserID) (*auth.User, error) {
	return &auth.User{ID: id, Username: "bruce"}, nil
}

type readableRoles struct{ rbac.Importer }

func (readableRoles) GetRole(_ context.Context, name rbac.RoleName) (*rbac.Role, error) {
	return &rbac.Role{Name: name}, nil
}

// writeOnlyAuth and writeOnlyRoles can only import.
type writeOnlyAuth struct{ auth.Importer }
type writeOnlyRoles struct{ rbac.Importer }

type nopAudit struct{ audit.Logger }

// TestImporters_NativeReadsForwardOnlyOnceBoundAndOnlyToAReader: the readback
// books' identity step needs (a key's owner, an earlier chunk's role) goes
// through the importer values it is handed. Unbound, it refuses with
// ErrImporterNotBound; bound to an importer that cannot read, with its own
// error (never auth.ErrNotFound, which would read as "no such user"); bound to
// a reader, it forwards.
func TestImporters_NativeReadsForwardOnlyOnceBoundAndOnlyToAReader(t *testing.T) {
	ctx := context.Background()
	l := &lateImporters{}
	imp := l.values()
	u, ok := imp.Auth.(interface {
		GetUser(context.Context, auth.UserID) (*auth.User, error)
	})
	if !ok {
		t.Fatal("Importers.Auth cannot read a user back")
	}
	r, ok := imp.Roles.(interface {
		GetRole(context.Context, rbac.RoleName) (*rbac.Role, error)
	})
	if !ok {
		t.Fatal("Importers.Roles cannot read a role back")
	}

	if got, err := u.GetUser(ctx, "01JBATCAVE0000000000000040"); got != nil || !errors.Is(err, module.ErrImporterNotBound) {
		t.Errorf("GetUser unbound = %v, %v; want nil and ErrImporterNotBound", got, err)
	}
	if got, err := r.GetRole(ctx, "viewer"); got != nil || !errors.Is(err, module.ErrImporterNotBound) {
		t.Errorf("GetRole unbound = %v, %v; want nil and ErrImporterNotBound", got, err)
	}

	l.bindIdentity(writeOnlyAuth{}, writeOnlyRoles{}, nopAudit{})
	if _, err := u.GetUser(ctx, "x"); !errors.Is(err, errNoNativeRead) || errors.Is(err, auth.ErrNotFound) {
		t.Errorf("GetUser over an importer that cannot read = %v; want errNoNativeRead and not ErrNotFound", err)
	}
	if _, err := r.GetRole(ctx, "viewer"); !errors.Is(err, errNoNativeRead) || errors.Is(err, rbac.ErrUnknownRole) {
		t.Errorf("GetRole over an importer that cannot read = %v; want errNoNativeRead and not ErrUnknownRole", err)
	}

	l.bindIdentity(readableAuth{}, readableRoles{}, nopAudit{})
	if got, err := u.GetUser(ctx, "01JBATCAVE0000000000000040"); err != nil || got == nil || got.ID != "01JBATCAVE0000000000000040" {
		t.Errorf("GetUser bound = %+v, %v", got, err)
	}
	if got, err := r.GetRole(ctx, "viewer"); err != nil || got == nil || got.Name != "viewer" {
		t.Errorf("GetRole bound = %+v, %v", got, err)
	}
}
