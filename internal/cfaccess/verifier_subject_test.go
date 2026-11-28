// file: internal/cfaccess/verifier_subject_test.go
// version: 0.1.0
// guid: 5b1e8d07-93c2-4a4f-8e66-d20c7f3a9b41
// last-edited: 2026-11-22

package cfaccess

import (
	"context"
	"strings"
	"testing"
)

// The subject the verifier hands to core's identity link (F4-08 B10e1c).
//
// The production Access link is keyed by the JWT `sub`: an opaque id that holds
// no '@' and is not the address. verifier.go falls back to the e-mail as the
// subject only when the token carries no `sub`. If that fallback ever applied to
// a token that does carry one, an Access sign-in would present a subject that
// matches no link, skip core's linked fast path, and meet the e-mail rule that
// refuses an address two administrators hold. These tests pin the rule from both
// sides: the sub wins whenever it exists, and the e-mail form appears only where
// there is no sub.

const subjectOpaqueSub = "d3a91b6c-7f20-4e58-b1c4-0a52e8f61937"

// TestVerifiedSubject_IsTheJWTSubWhenTheTokenCarriesOne: a human token with a sub
// verifies to Subject == that sub, never the address, whatever the address looks
// like and however the sub is shaped.
func TestVerifiedSubject_IsTheJWTSubWhenTheTokenCarriesOne(t *testing.T) {
	a := newAccess(t)
	v := a.verifier()
	const email = "bruce.wayne@example.test"
	for name, sub := range map[string]string{
		"opaque id":              subjectOpaqueSub,
		"short id":               "u1",
		"id naming the address":  "cf|" + email,
		"id holding an at sign":  "alfred@example.test",
		"id differing in case":   strings.ToUpper(email),
		"id equal to the e-mail": email, // a sub that is the address is the sub, not a fallback
	} {
		t.Run(name, func(t *testing.T) {
			got, err := v.VerifyIssued(context.Background(), a.token(tokenSpec{Sub: sub, Email: email}))
			if err != nil {
				t.Fatalf("VerifyIssued: %v", err)
			}
			if got.Claims.Subject != sub {
				t.Errorf("subject = %q, want the token's sub %q", got.Claims.Subject, sub)
			}
			if got.Claims.Email != email {
				t.Errorf("email = %q, want %q", got.Claims.Email, email)
			}
		})
	}
}

// TestVerifiedSubject_OpaqueSubIsNotTheEmailForm: for the production shape (an
// opaque sub, a different address) the subject holds no '@' and is not the
// address. Verify (the HEAD-shaped entry point) and VerifyIssued (the extension's)
// agree.
func TestVerifiedSubject_OpaqueSubIsNotTheEmailForm(t *testing.T) {
	a := newAccess(t)
	v := a.verifier()
	const email = "bruce.wayne@example.test"
	raw := a.token(tokenSpec{Sub: subjectOpaqueSub, Email: email})
	issued, err := v.VerifyIssued(context.Background(), raw)
	if err != nil {
		t.Fatalf("VerifyIssued: %v", err)
	}
	plain, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for name, subject := range map[string]string{"VerifyIssued": issued.Claims.Subject, "Verify": plain.Subject} {
		if strings.Contains(subject, "@") {
			t.Errorf("%s: the subject %q holds an '@': it is the e-mail form", name, subject)
		}
		if strings.EqualFold(subject, email) {
			t.Errorf("%s: the subject %q is the address", name, subject)
		}
		if subject != subjectOpaqueSub {
			t.Errorf("%s: subject = %q, want %q", name, subject, subjectOpaqueSub)
		}
	}
}

// TestVerifiedSubject_EmailIsTheSubjectOnlyWhenThereIsNoSub: the fallback exists
// and applies exactly to a token with no sub, so the e-mail form is reachable only
// there.
func TestVerifiedSubject_EmailIsTheSubjectOnlyWhenThereIsNoSub(t *testing.T) {
	a := newAccess(t)
	v := a.verifier()
	const email = "bruce.wayne@example.test"
	got, err := v.VerifyIssued(context.Background(), a.token(tokenSpec{Sub: "", Email: email}))
	if err != nil {
		t.Fatalf("VerifyIssued: %v", err)
	}
	if got.Claims.Subject != email {
		t.Errorf("a token with no sub: subject = %q, want the e-mail %q (the documented fallback)", got.Claims.Subject, email)
	}
	if got.Claims.Provider != "cfaccess" || !got.Claims.EmailVerified {
		t.Errorf("claims = %+v, want provider cfaccess and a verified address", got.Claims)
	}
}
