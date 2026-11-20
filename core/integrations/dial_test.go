// file: core/integrations/dial_test.go
// version: 1.0.0
// guid: 8c41d7e3-02fa-4b69-b5d1-6a9e307c4f18
// last-edited: 2026-11-19

package integrations_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
)

// dialGate is a Gate that is also a GuardedDialer: it records what the
// Registry's wrapper forwarded and answers with one end of a pipe.
type dialGate struct {
	integrations.Gate
	mu    sync.Mutex
	calls []string
}

func (g *dialGate) DialContext(_ context.Context, network, addr string) (net.Conn, error) {
	g.mu.Lock()
	g.calls = append(g.calls, network+" "+addr)
	g.mu.Unlock()
	a, b := net.Pipe()
	go func() { _ = b.Close() }()
	return a, nil
}

func (g *dialGate) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

func refusedLocal(ctx context.Context, err error) bool {
	return integrations.ClassOf(ctx, err) == integrations.ClassRefusedLocal
}

// TestGuardedDial_NoOutboundRefuses: a Registry built without an outbound
// layer has no egress policy, so an instance's gate refuses to dial.
func TestGuardedDial_NoOutboundRefuses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, func(o *integrations.Options) { o.Outbound = nil })
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	inst, release := lease(t, r, signalRef)
	defer release()
	d, ok := inst.Env.Gate.(integrations.GuardedDialer)
	if !ok {
		t.Fatalf("the Env gate %T offers no guarded dial: an adapter would find none", inst.Env.Gate)
	}
	conn, err := d.DialContext(ctx, "tcp", "192.0.2.20:587")
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || !refusedLocal(ctx, err) {
		t.Fatalf("dial without an outbound policy: %v", err)
	}
}

// TestGuardedDial_GateWithoutDialRefuses: an outbound layer whose gate cannot
// guard a dial must not turn the Registry's wrapper into an unguarded one.
func TestGuardedDial_GateWithoutDialRefuses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t) // integrationstest's fake gate offers no dial
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	inst, release := lease(t, r, signalRef)
	defer release()
	conn, err := integrations.GuardedDial(inst.Env.Gate, signalRef)(ctx, "tcp", "192.0.2.20:587")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, integrations.ErrNoEgressGuard) || !refusedLocal(ctx, err) {
		t.Fatalf("dial through a gate with no guard: %v", err)
	}
}

// TestGuardedDial_ForwardsToTheInstancesGate: the Env gate is the Registry's
// wrapper around the outbound layer's gate; the dial reaches the inner gate,
// with the network and address unchanged, so the inner gate's egress policy is
// the one applied.
func TestGuardedDial_ForwardsToTheInstancesGate(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	inner := &dialGate{}
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.Outbound = func(s integrations.OutboundSpec) (integrations.HTTP, integrations.Gate) {
			h, g := e.out.Func()(s)
			inner.Gate = g
			return h, inner
		}
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	inst, release := lease(t, r, signalRef)
	defer release()
	conn, err := integrations.GuardedDial(inst.Env.Gate, signalRef)(ctx, "tcp", "mail.gotham.test:587")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if got := inner.seen(); len(got) != 1 || got[0] != "tcp mail.gotham.test:587" {
		t.Fatalf("the inner gate saw %v", got)
	}
}

// TestGuardedDial_DisabledRefusesBeforeTheInnerGate: the global switch stops a
// dial the way it stops Acquire and Do, without asking the inner gate.
func TestGuardedDial_DisabledRefusesBeforeTheInnerGate(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var disabled atomic.Bool
	inner := &dialGate{}
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.Settings = func() integrations.Settings { return integrations.Settings{Disabled: disabled.Load()} }
		o.Outbound = func(s integrations.OutboundSpec) (integrations.HTTP, integrations.Gate) {
			h, g := e.out.Func()(s)
			inner.Gate = g
			return h, inner
		}
	})
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	inst, release := lease(t, r, signalRef)
	defer release()
	disabled.Store(true)
	conn, err := integrations.GuardedDial(inst.Env.Gate, signalRef)(ctx, "tcp", "mail.gotham.test:587")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, integrations.ErrDisabled) || !refusedLocal(ctx, err) {
		t.Fatalf("dial while disabled: %v", err)
	}
	if got := inner.seen(); len(got) != 0 {
		t.Fatalf("the inner gate was asked while disabled: %v", got)
	}
}

// TestGuardedDial_NoGateRefuses: an adapter built with no gate at all (nil) or
// with a gate that has no dial gets a dial that refuses; it is never a plain
// net dial.
func TestGuardedDial_NoGateRefuses(t *testing.T) {
	ctx := context.Background()
	ref := integrations.Ref{Kind: integrations.KindNotify, Instance: "commissioner"}
	for name, g := range map[string]integrations.Gate{"nil": nil, "a gate with no dial": plainGate{}} {
		conn, err := integrations.GuardedDial(g, ref)(ctx, "tcp", "192.0.2.20:587")
		if conn != nil {
			_ = conn.Close()
		}
		if !errors.Is(err, integrations.ErrNoEgressGuard) || !refusedLocal(ctx, err) {
			t.Errorf("%s: err = %v", name, err)
		}
		if e, ok := errors.AsType[*integrations.Error](err); !ok || e.Ref != ref || e.Op != "dial" {
			t.Errorf("%s: the refusal is %#v, want an *Error for the ref with op dial", name, err)
		}
	}
}

type plainGate struct{}

func (plainGate) Acquire(context.Context, string) (integrations.Ticket, error) { return nil, nil }
func (plainGate) State() integrations.GateState                                { return integrations.GateState{} }
