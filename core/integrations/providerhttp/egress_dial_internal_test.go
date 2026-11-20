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

// TestGuardedDial_RefusalCountsAndGivesBackItsToken: the guard's refusal of a
// dial is counted as an egress refusal (the same instrument an HTTP refusal
// feeds) and gives back the bucket token the connection's Acquire took, as a
// refused HTTP attempt does. A lookup that fails is neither: the destination was
// not judged, so it is not counted as a refusal and the token stays spent.
func TestGuardedDial_RefusalCountsAndGivesBackItsToken(t *testing.T) {
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
	d := g.(integrations.GuardedDialer)
	tokens := func() float64 { return g.State().BucketTokens }

	dialUnderTicket := func(addr string) error {
		t.Helper()
		before := tokens()
		if _, err := g.Acquire(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		if got := tokens(); got > before-0.9 {
			t.Fatalf("Acquire took no token: %.2f -> %.2f", before, got)
		}
		_, err := d.DialContext(context.Background(), "tcp", addr)
		if err == nil {
			t.Fatalf("%s: dialled", addr)
		}
		return err
	}

	// A refusal by the guard: counted, token back.
	if err := dialUnderTicket("mail.gotham.test:587"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
	if n := refusedCount(t, rd, ReasonEgress); n != 1 {
		t.Fatalf("egress refusals counted = %d, want 1", n)
	}
	if got := tokens(); got < 1.9 {
		t.Fatalf("tokens = %.2f after a refused dial, want the token back (2)", got)
	}

	// An address that is not host:port is refused the same way.
	if err := dialUnderTicket("mail.gotham.test"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if n := refusedCount(t, rd, ReasonEgress); n != 2 {
		t.Fatalf("egress refusals counted = %d, want 2", n)
	}
	if got := tokens(); got < 1.9 {
		t.Fatalf("tokens = %.2f after a malformed address, want the token back (2)", got)
	}

	// A lookup that fails is not a refusal and does not give the token back.
	if err := dialUnderTicket("relay.gotham.test:587"); errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, a failed lookup is not a refusal", err)
	}
	if n := refusedCount(t, rd, ReasonEgress); n != 2 {
		t.Fatalf("egress refusals counted = %d after a failed lookup, want still 2", n)
	}
	if got := tokens(); got > 1.1 {
		t.Fatalf("tokens = %.2f after a failed lookup, want the token spent (1)", got)
	}
}
