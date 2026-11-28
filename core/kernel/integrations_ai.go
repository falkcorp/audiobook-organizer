// file: core/kernel/integrations_ai.go
// version: 0.1.0
// guid: 4b7efa65-b9df-44ce-83b4-d9c074b6dff9
// last-edited: 2026-11-22

package kernel

import (
	"context"

	"github.com/falkcorp/mediacore/integrations/ai"
)

// aiConfig is core.ai as ai.Config. A kind limit of 0 is none, so only the
// kinds that name one are in the map.
func aiConfig(v aiSettings) ai.Config {
	c := ai.Config{
		CooldownBase:          v.CooldownBase.Std(),
		CooldownMax:           v.CooldownMax.Std(),
		QuotaCooldown:         v.QuotaCooldown.Std(),
		DeadlineFailovers:     v.DeadlineFailovers,
		RepairAttempts:        v.Structured.RepairAttempts,
		CapsTTL:               v.CapsTTL.Std(),
		BatchParallelism:      v.BatchParallelism,
		BatchMaxApplyAttempts: v.BatchMaxApplyAttempts,
		LogNoCapableEvery:     v.LogNoCapableEvery.Std(),
	}
	for k, n := range map[ai.Kind]int{ai.KindText: v.KindLimits.Text, ai.KindEmbed: v.KindLimits.Embed, ai.KindTranscribe: v.KindLimits.Transcribe} {
		if n > 0 {
			if c.KindLimits == nil {
				c.KindLimits = map[ai.Kind]int{}
			}
			c.KindLimits[k] = n
		}
	}
	return c
}

// buildAI builds the ai Router over the Registry, with the durable batch job
// store over c/ai/ and the core metrics scope <namespace>.ai (the keys the
// router declares, ai.ScopeKeys). The router reads core.integrations.disabled
// per call, so a copy of a store never spends a provider's budget. Stager is
// the kernel's stage-only starter (a nil one leaves the poll unable to stage an
// apply run and failing closed); a typed nil is normalized to nil.
func (s *integrationsServices) buildAI(_ context.Context) error {
	ks, err := s.keyspace(ai.Owner)
	if err != nil {
		return err
	}
	sc, err := s.scope("ai", ai.ScopeKeys()...)
	if err != nil {
		return err
	}
	opts := ai.RouterOptions{
		Registry: s.registry,
		Disabled: s.disabled,
		Config: func() ai.Config {
			if s.in.AI == nil {
				return ai.Config{}
			}
			return aiConfig(s.in.AI())
		},
		Jobs:     ai.NativeJobStore(ks),
		NewBatch: ks.NewIndexedBatch,
		Metrics:  sc,
		Now:      s.in.now(),
		Logger:   s.in.logger("ai"),
	}
	if !isNil(s.in.Stager) {
		opts.Stager = s.in.Stager
	}
	r, err := ai.NewRouter(opts)
	if err != nil {
		return err
	}
	s.ai = r
	s.built("ai", func(context.Context) error { return r.Close() })
	return nil
}

// freezeAI closes the router's endpoint catalog (05-ai: every module registers
// its AI endpoints in P6, so the freeze is P7's, after the last Register).
func (s *integrationsServices) freezeAI() { s.ai.FreezeCatalog() }
