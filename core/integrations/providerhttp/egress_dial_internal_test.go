// file: core/integrations/providerhttp/egress_dial_internal_test.go
// version: 1.0.0
// guid: 8d2c6f41-3b97-4e50-a6d8-1f4b9c70e532
// last-edited: 2026-11-19

package providerhttp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/mediacore/integrations"
)

// refusedCount is the sum of the refused counter for reason.
func refusedCount(t *testing.T, rd *sdkmetric.ManualReader, reason Reason) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := rd.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "refused" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				if v, ok := dp.Attributes.Value("reason"); ok && v.AsString() == string(reason) {
					n += dp.Value
				}
			}
		}
	}
	return n
}

// dialRig is a registry whose gate has a bucket of two tokens that effectively
// never refills, a resolver that puts mail.gotham.test on a private address and
// fails every other lookup, and a metrics sink the test can read.
func dialRig(t *testing.T) (integrations.Gate, integrations.GuardedDialer, *sdkmetric.ManualReader) {
	t.Helper()
	rd := sdkmetric.NewManualReader()
	m := sdkmetric.NewMeterProvider(sdkmetric.WithReader(rd)).Meter("test")
	in := &instruments{}
	var err error
	if in.requests, err = m.Int64Counter("requests"); err != nil {
		t.Fatal(err)
	}
	if in.refusals, err = m.Int64Counter("refused"); err != nil {
		t.Fatal(err)
	}
	if in.retried, err = m.Int64Counter("retries"); err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "mail.gotham.test":
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		}
		return nil, errors.New("lookup failed")
	}
	r, err := New(Options{
		Resolver: lookup,
		DialAddr: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("never dialled") },
		LANAllow: func() []string { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	r.met = in
	_, g := r.Outbound(integrations.OutboundSpec{
		Ref:    integrations.Ref{Kind: integrations.KindNotify, Instance: "commissioner"},
		Egress: integrations.EgressConfigured, Limits: integrations.Limits{RPS: 0.0001, Burst: 2},
	})
	return g, g.(integrations.GuardedDialer), rd
}

func acquire(t *testing.T, g integrations.Gate) integrations.Ticket {
	t.Helper()
	before := g.State().BucketTokens
	tk, err := g.Acquire(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := g.State().BucketTokens; got > before-0.9 {
		t.Fatalf("Acquire took no token: %.2f -> %.2f", before, got)
	}
	return tk
}

func dial(t *testing.T, d integrations.GuardedDialer, addr string) error {
	t.Helper()
	_, err := d.DialContext(context.Background(), "tcp", addr)
	if err == nil {
		t.Fatalf("%s: dialled", addr)
	}
	return err
}

// TestGuardedDial_RefusalCountsAndTheTicketGivesBackItsToken: the guard's
// refusal of a dial is counted as an egress refusal (the same instrument an HTTP
// refusal feeds), and the Ticket of the Acquire that took the token gives it back
// when Done is handed the refusal, as a refused HTTP attempt does. A lookup that
// fails is neither: the destination was not judged, so it is not counted as a
// refusal and the token stays spent.
func TestGuardedDial_RefusalCountsAndTheTicketGivesBackItsToken(t *testing.T) {
	g, d, rd := dialRig(t)
	tokens := func() float64 { return g.State().BucketTokens }

	// A refusal by the guard: counted, and the token is back once Done has it.
	tk := acquire(t, g)
	err := dial(t, d, "mail.gotham.test:587")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
	if n := refusedCount(t, rd, ReasonEgress); n != 1 {
		t.Fatalf("egress refusals counted = %d, want 1", n)
	}
	if got := tokens(); got > 1.1 {
		t.Fatalf("tokens = %.2f before Done: the dial itself must not give the token back (1)", got)
	}
	tk.Done(err)
	if got := tokens(); got < 1.9 {
		t.Fatalf("tokens = %.2f after Done(refusal), want the token back (2)", got)
	}

	// An address that is not host:port is refused the same way.
	tk = acquire(t, g)
	err = dial(t, d, "mail.gotham.test")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if n := refusedCount(t, rd, ReasonEgress); n != 2 {
		t.Fatalf("egress refusals counted = %d, want 2", n)
	}
	tk.Done(err)
	if got := tokens(); got < 1.9 {
		t.Fatalf("tokens = %.2f after a malformed address, want the token back (2)", got)
	}

	// A lookup that fails is not a refusal and does not give the token back.
	tk = acquire(t, g)
	err = dial(t, d, "relay.gotham.test:587")
	if errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, a failed lookup is not a refusal", err)
	}
	tk.Done(err)
	if n := refusedCount(t, rd, ReasonEgress); n != 2 {
		t.Fatalf("egress refusals counted = %d after a failed lookup, want still 2", n)
	}
	if got := tokens(); got > 1.1 {
		t.Fatalf("tokens = %.2f after a failed lookup, want the token spent (1)", got)
	}
}

// TestGuardedDial_NoAcquireNoRefund: a refused dial made without an Acquire has
// no reservation to give a token back for, so it returns nothing: the bucket is
// drained by two real Acquires, and a refused dial under neither must not mint a
// token.
func TestGuardedDial_NoAcquireNoRefund(t *testing.T) {
	g, d, _ := dialRig(t)
	a, b := acquire(t, g), acquire(t, g)
	if got := g.State().BucketTokens; got > 0.1 {
		t.Fatalf("tokens = %.2f after two Acquires, want ~0", got)
	}
	for _, addr := range []string{"mail.gotham.test:587", "mail.gotham.test"} {
		dial(t, d, addr)
		if got := g.State().BucketTokens; got > 0.1 {
			t.Fatalf("tokens = %.2f after a refused dial with no Acquire (%s): a token nobody took was returned", got, addr)
		}
	}
	a.Done(nil)
	b.Done(nil)
	if got := g.State().BucketTokens; got > 0.1 {
		t.Fatalf("tokens = %.2f after two successful Dones, want ~0 (a success gives nothing back)", got)
	}
}

// TestGuardedDial_TwoDialsOneAcquireRefundOnce: a sender that dials twice under
// one Acquire (a second host to try, an internal retry) gets back the one token
// that Acquire took, not one per refused dial; and Done being called again
// returns nothing more (the ticket's once).
func TestGuardedDial_TwoDialsOneAcquireRefundOnce(t *testing.T) {
	g, d, _ := dialRig(t)
	a, b := acquire(t, g), acquire(t, g) // drained: 0 tokens, so a minted token shows
	err := dial(t, d, "mail.gotham.test:587")
	if err2 := dial(t, d, "mail.gotham.test:25"); !errors.Is(err2, ErrBlockedAddress) {
		t.Fatalf("second dial err = %v", err2)
	}
	if got := g.State().BucketTokens; got > 0.1 {
		t.Fatalf("tokens = %.2f after two refused dials, before Done", got)
	}
	a.Done(err)
	if got := g.State().BucketTokens; got < 0.9 || got > 1.1 {
		t.Fatalf("tokens = %.2f after Done(refusal), want exactly the one token back (1)", got)
	}
	a.Done(err)
	if got := g.State().BucketTokens; got > 1.1 {
		t.Fatalf("tokens = %.2f after a second Done on the same ticket, want still 1", got)
	}
	b.Done(nil)
}

// TestGuardedDial_OnlyAnEgressRefusalGivesTheTokenBack: a ticket's Done refunds
// the bucket token for the guard's refusal of the dial and for nothing else: not
// a different kind of gate refusal, and not an ordinary failure.
func TestGuardedDial_OnlyAnEgressRefusalGivesTheTokenBack(t *testing.T) {
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: "commissioner"}
	for name, err := range map[string]error{
		"another gate refusal": refuse(ref, "dial", ReasonDisabled, time.Time{}, integrations.ErrDisabled),
		"an ordinary failure":  errors.New("connection reset"),
		"success":              nil,
	} {
		t.Run(name, func(t *testing.T) {
			g, _, _ := dialRig(t) // a fresh bucket of two per case: a second Acquire would wait on a refill
			tk := acquire(t, g)
			before := g.State().BucketTokens
			tk.Done(err)
			if got := g.State().BucketTokens; got > before+0.1 {
				t.Fatalf("tokens %.2f -> %.2f, Done gave a token back", before, got)
			}
		})
	}
}
