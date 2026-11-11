// file: core/integrations/errors_test.go
// version: 1.0.0
// guid: f3b555ad-d764-471b-8793-931f7b032e28
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// TestClassOf_Table (M1.5): ClassOf's order, with the done-parent rule
// first: a done caller ctx is ClassCanceled whatever the error says.
func TestClassOf_Table(t *testing.T) {
	live := context.Background()
	done, cancel := context.WithCancel(live)
	cancel()
	auth := &integrations.Error{Ref: signalRef, Op: "send", Class: integrations.ClassAuth, Status: 401}
	for _, c := range []struct {
		name   string
		parent context.Context
		err    error
		want   integrations.Class
	}{
		{"nil", live, nil, integrations.ClassUnknown},
		{"done parent beats the error's class", done, auth, integrations.ClassCanceled},
		{"done parent beats a transport error", done, io.ErrUnexpectedEOF, integrations.ClassCanceled},
		{"nil parent", nil, auth, integrations.ClassAuth},
		{"*Error", live, auth, integrations.ClassAuth},
		{"wrapped *Error", live, fmt.Errorf("send: %w", &integrations.Error{Class: integrations.ClassQuota}), integrations.ClassQuota},
		{"disabled", live, integrations.ErrDisabled, integrations.ClassRefusedLocal},
		{"withheld", live, integrations.ErrOperatorSecretWithheld, integrations.ClassRefusedLocal},
		{"not configured", live, fmt.Errorf("x: %w", integrations.ErrNotConfigured), integrations.ClassRefusedLocal},
		{"unknown adapter", live, integrations.ErrUnknownAdapter, integrations.ClassRefusedLocal},
		{"canceled, live parent", live, context.Canceled, integrations.ClassCanceled},
		{"deadline, live parent", live, context.DeadlineExceeded, integrations.ClassTransient},
		{"op error", live, &net.OpError{Op: "dial", Err: errors.New("no route")}, integrations.ClassTransient},
		{"dns error", live, &net.DNSError{Err: "no such host", Name: "gotham.invalid"}, integrations.ClassTransient},
		{"refused", live, fmt.Errorf("dial: %w", syscall.ECONNREFUSED), integrations.ClassTransient},
		{"reset", live, syscall.ECONNRESET, integrations.ClassTransient},
		{"truncated", live, io.ErrUnexpectedEOF, integrations.ClassTransient},
		{"anything else", live, errors.New("the Riddler's riddle"), integrations.ClassUnknown},
	} {
		if got := integrations.ClassOf(c.parent, c.err); got != c.want {
			t.Errorf("%s: ClassOf = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestClass_Predicates pins each class's name and its two predicates.
func TestClass_Predicates(t *testing.T) {
	for _, c := range []struct {
		class           integrations.Class
		name            string
		retryable, wear bool
	}{
		{integrations.ClassUnknown, "unknown", false, false},
		{integrations.ClassCanceled, "canceled", false, false},
		{integrations.ClassRefusedLocal, "refused_local", false, false},
		{integrations.ClassTransient, "transient", true, true},
		{integrations.ClassRateLimited, "rate_limited", true, true},
		{integrations.ClassQuota, "quota", false, true},
		{integrations.ClassAuth, "auth", false, true},
		{integrations.ClassNotFound, "not_found", false, false},
		{integrations.ClassInvalid, "invalid", false, false},
		{integrations.ClassUnsupported, "unsupported", false, false},
		{integrations.ClassBadOutput, "bad_output", false, false},
		{integrations.Class(99), "class(99)", false, false},
	} {
		if c.class.String() != c.name || c.class.Retryable() != c.retryable || c.class.CountsAgainstRemote() != c.wear {
			t.Errorf("%d: %q retryable=%v counts=%v", c.class, c.class.String(), c.class.Retryable(), c.class.CountsAgainstRemote())
		}
	}
	for s, want := range map[integrations.HealthStatus]string{
		integrations.HealthOK: "ok", integrations.HealthDegraded: "degraded", integrations.HealthDown: "down",
		integrations.HealthAuthFailed: "auth_failed", integrations.HealthMisconfigured: "misconfigured",
		integrations.HealthDisabled: "disabled", integrations.HealthStatus(42): "unknown",
	} {
		if s.String() != want {
			t.Errorf("HealthStatus(%d) = %q, want %q", s, s.String(), want)
		}
	}
}

// TestError_RedactsDetail (M1.3): an Error's string, in every fmt verb and
// wrapped, never carries its Detail, the remote body excerpt.
func TestError_RedactsDetail(t *testing.T) {
	inner := errors.New("401 Unauthorized")
	e := &integrations.Error{
		Ref: signalRef, Op: "send", Class: integrations.ClassAuth, Status: 401, RetryAfter: time.Minute,
		Detail: `{"error":"bad key","echo":"JOKER-CARD-0451"}`, Err: inner,
	}
	if got, want := e.Error(), "integrations: notify/gcpd-roof send: auth (status 401): 401 Unauthorized"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	wrapped := fmt.Errorf("notify: %w", e)
	for _, s := range []string{
		e.Error(), fmt.Sprint(e), fmt.Sprintf("%v %+v %s %q %#v", e, e, e, e, e), wrapped.Error(), fmt.Sprintf("%+v", wrapped),
	} {
		if strings.Contains(s, "JOKER-CARD") {
			t.Errorf("the detail leaked: %s", s)
		}
	}
	if !errors.Is(e, inner) || !errors.Is(wrapped, inner) {
		t.Error("Unwrap lost the cause")
	}
	if bare := (&integrations.Error{Class: integrations.ClassTransient}).Error(); bare != "integrations: /: transient" {
		t.Errorf("a bare error = %q", bare)
	}
	if integrations.MaxDetail != 512 {
		t.Errorf("MaxDetail = %d", integrations.MaxDetail)
	}
}

// TestIDs_Grammar pins the ID grammars: an instance ID needs a leading
// letter (F7-01: its secrets need one), an adapter ID its module prefix.
func TestIDs_Grammar(t *testing.T) {
	for id, ok := range map[integrations.InstanceID]bool{
		"batcave": true, "gcpd-roof": true, "b": true, "robin2": true,
		"": false, "0dark": false, "-joker": false, "Batcave": false, "bat_cave": false,
		"a2345678901234567890123456789012": true, "a23456789012345678901234567890123": false,
	} {
		if err := id.Validate(); (err == nil) != ok {
			t.Errorf("InstanceID %q: err = %v, want ok=%v", id, err, ok)
		}
	}
	for id, ok := range map[integrations.AdapterID]bool{
		"core.qbittorrent": true, "hello.batsignal": true, "hello.bat_signal-2": true,
		"batsignal": false, "h.batsignal": false, "core.": false, "Core.x": false, "hello.2x": false,
		"thisisaverylongmodule.x": false, "hello." + integrations.AdapterID(strings.Repeat("a", 65)): false,
	} {
		if err := id.Validate(); (err == nil) != ok {
			t.Errorf("AdapterID %q: err = %v, want ok=%v", id, err, ok)
		}
	}
	if p := integrations.AdapterID("batsignal").Prefix(); p != "" {
		t.Errorf("Prefix of an unprefixed ID = %q", p)
	}
	if got := integrations.Kinds(); len(got) != 5 || !got[0].Valid() || integrations.Kind("gadgets").Valid() {
		t.Errorf("Kinds = %v", got)
	}
	if signalRef.String() != "notify/gcpd-roof" {
		t.Errorf("Ref.String = %q", signalRef.String())
	}
}
