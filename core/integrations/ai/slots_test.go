// file: core/integrations/ai/slots_test.go
// version: 1.0.0
// guid: 04e02755-7db6-4e62-b225-4a9f0e9dd1d8
// last-edited: 2026-11-12

package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

func slotRef(name string) integrations.Ref {
	return integrations.Ref{Kind: integrations.KindAI, Instance: integrations.InstanceID(name)}
}

// The kind cap spans endpoints and is the only thing that can refuse a slot an
// endpoint still has: pinned here with the non-blocking acquire, so no goroutine
// or timer decides the outcome.
func TestSlots_KindCapSpansEndpointsAndOnlyItsKind(t *testing.T) {
	t.Parallel()
	s := newSlots()
	arkham, blackgate := slotRef("arkham"), slotRef("blackgate")

	relA, ok := s.tryAcquire(arkham, 4, KindText, 1)
	if !ok {
		t.Fatal("the first text slot was refused")
	}
	if _, ok := s.tryAcquire(blackgate, 4, KindText, 1); ok {
		t.Error("a second endpoint gave a text slot beyond kind_limits.text=1")
	}
	if _, ok := s.tryAcquire(arkham, 4, KindText, 1); ok {
		t.Error("the same endpoint gave a text slot beyond kind_limits.text=1")
	}
	relEmbed, ok := s.tryAcquire(blackgate, 4, KindEmbed, 1)
	if !ok {
		t.Error("a text cap refused embed work")
	}
	if _, ok := s.tryAcquire(blackgate, 4, KindText, 0); !ok {
		t.Error("a kind limit of 0 (unlimited) was enforced")
	}
	relA()
	relA() // a release is safe to repeat: it must not give the slot back twice
	if got := s.byKind[KindText]; got != 1 {
		t.Errorf("text slots held after a doubled release = %d, want the 1 taken by the unlimited acquire", got)
	}
	relEmbed()
}

// Every release wakes a waiter, and a waiter whose context ends takes nothing.
func TestSlots_AcquireWaitsForARelease(t *testing.T) {
	t.Parallel()
	s := newSlots()
	ref := slotRef("arkham")
	rel, ok := s.tryAcquire(ref, 1, KindText, 0)
	if !ok {
		t.Fatal("the slot was refused")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.acquire(ctx, ref, 1, KindText, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire on a full endpoint = %v, want the context's deadline", err)
	}
	if got := s.depth(ref); got != 1 {
		t.Fatalf("depth after a waiter gave up = %d, want 1: a waiter that gives up must take nothing", got)
	}

	done := make(chan error, 1)
	go func() {
		r2, err := s.acquire(context.Background(), ref, 1, KindText, 0)
		if err == nil {
			r2()
		}
		done <- err
	}()
	rel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acquire after a release = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter was never woken by the release")
	}
	if got := s.depth(ref); got != 0 {
		t.Errorf("depth at the end = %d, want 0", got)
	}
}
