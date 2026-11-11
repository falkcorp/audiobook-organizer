// file: core/auth/external_test.go
// version: 1.0.0
// guid: a8a169fa-937d-4022-9867-330ea67cb655
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// The e-mail lock auth/oidc takes is the one CreateUser holds: while it is
// held, a creation waits, so "no other account holds this address verified"
// cannot be raced from either side.
func TestLockEmails_ExcludesCreateUser(t *testing.T) {
	f := newFx(t)
	admin := &auth.User{ID: "u-bruce", Username: "bruce", Roles: []rbac.RoleName{rbac.RoleAdmin}, Status: auth.StatusActive}
	p := &auth.Principal{User: admin, Method: auth.MethodSession,
		Perms: f.rb.Compile(rbac.CheckerInput{Roles: admin.Roles, Interactive: true})}

	unlock := f.svc.LockEmails()
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.CreateUser(context.Background(), p, auth.NewUser{Username: "alfred", Email: "alfred@example.com"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("CreateUser finished while the e-mail lock was held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CreateUser after the lock was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CreateUser never finished after the lock was released")
	}
}

func TestNoteUserCreated_EndsFirstRun(t *testing.T) {
	f := newFx(t)
	if f.svc.HasUsers() {
		t.Fatal("a fresh store reports users")
	}
	f.svc.NoteUserCreated()
	if !f.svc.HasUsers() {
		t.Fatal("HasUsers is false after NoteUserCreated")
	}
}
