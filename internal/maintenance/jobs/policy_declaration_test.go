// file: internal/maintenance/jobs/policy_declaration_test.go
// version: 1.0.1
// guid: 6d2f8b41-9e73-4c05-a8d6-1b47e903fa25
// last-edited: 2026-08-17

package jobs_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	_ "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// wantJobCount is the number of registered maintenance jobs. Verified five ways on
// 2026-08-17: 37 `maintenance.Register` calls under internal/maintenance/jobs, 37
// files containing one, 37 Run receivers, 37 non-test job files — and, most
// conclusively, exactly 37 compiler errors when Policy() was first added to the
// MaintenanceJob interface.
//
// A four-way agreement is not what makes this trustworthy; a naming-shaped survey
// of the same population returned 35 and was wrong. The compiler is the instrument
// that cannot miss one, because a job that fails to implement the interface cannot
// build.
const wantJobCount = 37

// TestEveryJobDeclaresAUsablePolicy is the reason ExecutionPolicy can be a struct
// rather than five separate interface methods.
//
// The compile break from adding Policy() guarantees every job AUTHOR was forced to
// answer, but not that the ANSWER is usable: `return maintenance.ExecutionPolicy{}`
// compiles cleanly and yields ResumeUnspecified and LivenessUnspecified, both of
// which are `= iota` = 0. RegisterOp rejects each (registry.go:433-434) — but only
// at server startup, which in PR-2 means a failed deploy rather than a failed build.
//
// This test moves that failure to `go test`. Do not delete it without first
// converting ExecutionPolicy's fields into separate interface methods.
func TestEveryJobDeclaresAUsablePolicy(t *testing.T) {
	jobs := maintenance.All()
	if len(jobs) == 0 {
		t.Fatal("no maintenance jobs registered; this test would pass vacuously")
	}
	if len(jobs) != wantJobCount {
		t.Errorf("registered job count = %d, want %d — if a job was added or removed "+
			"deliberately, update wantJobCount; if not, one was silently dropped",
			len(jobs), wantJobCount)
	}

	for _, job := range jobs {
		t.Run(job.ID(), func(t *testing.T) {
			p := job.Policy()

			if p.ResumePolicy == opsregistry.ResumeUnspecified {
				t.Errorf("ResumePolicy is ResumeUnspecified (the zero value); "+
					"RegisterOp would reject %q at server startup", job.ID())
			}
			if p.Liveness == opsregistry.LivenessUnspecified {
				t.Errorf("Liveness is LivenessUnspecified (the zero value); "+
					"RegisterOp would reject %q at server startup", job.ID())
			}
			if p.Timeout <= 0 {
				t.Errorf("Timeout is %v; a non-positive timeout means the registry "+
					"default silently applies instead of the declared 4h", p.Timeout)
			}
			if len(p.Capabilities) == 0 {
				t.Errorf("Capabilities is empty; every job reached through the bridge " +
					"holds library read+write today, so empty is a regression")
			}
		})
	}
}

// TestPolicyIsBehaviourPreservingVersusTheBridge pins PR-1's central claim: that
// these declarations change nothing, because they restate what
// internal/server/maintenance_job_op.go already hardcodes for all 37 jobs.
//
// The four documented exceptions are listed explicitly rather than skipped, so that
// a fifth job quietly drifting off the bridge's policy fails here instead of being
// discovered in PR-2 as an unexplained behaviour change.
func TestPolicyIsBehaviourPreservingVersusTheBridge(t *testing.T) {
	// What the bridge hardcodes today (maintenance_job_op.go:38-42).
	const (
		bridgeResume         = opsregistry.ResumeDrop
		bridgeLiveness       = opsregistry.LivenessManual
		bridgeConcurrencyKey = ""
	)

	// The only jobs permitted to differ, and in the only field they may differ in.
	wantResumeOverride := map[string]opsregistry.ResumePolicy{
		"backfill-file-hashes":      opsregistry.ResumeRestart,
		"recompute-book-aggregates": opsregistry.ResumeRestart,
		"retention-and-hygiene":     opsregistry.ResumeRestart,
		"bulk-fetch-metadata":       opsregistry.ResumeRequeue,
	}

	jobs := maintenance.All()
	if len(jobs) == 0 {
		t.Fatal("no maintenance jobs registered; this test would pass vacuously")
	}

	seenOverrides := 0
	for _, job := range jobs {
		t.Run(job.ID(), func(t *testing.T) {
			p := job.Policy()

			if p.Liveness != bridgeLiveness {
				t.Errorf("Liveness = %v, want %v (the bridge's value); PR-1 is "+
					"behaviour-preserving and does not change liveness", p.Liveness, bridgeLiveness)
			}
			if p.ConcurrencyKey != bridgeConcurrencyKey {
				t.Errorf("ConcurrencyKey = %q, want %q; the bridge allows all 37 to run "+
					"concurrently today, so a key here is a behaviour change and belongs in PR-2",
					p.ConcurrencyKey, bridgeConcurrencyKey)
			}

			want, isOverride := wantResumeOverride[job.ID()]
			if !isOverride {
				want = bridgeResume
			}
			if p.ResumePolicy != want {
				t.Errorf("ResumePolicy = %v, want %v", p.ResumePolicy, want)
			}
			if isOverride {
				seenOverrides++
			}
		})
	}

	if seenOverrides != len(wantResumeOverride) {
		t.Errorf("matched %d of %d declared resume overrides; a job ID in "+
			"wantResumeOverride does not exist, so its override is not being checked",
			seenOverrides, len(wantResumeOverride))
	}
}

// TestPolicyAgreesWithCanResume documents the exact permitted disagreements between
// the old CanResume() bool and the new Policy().ResumePolicy, so the gap is a
// reviewed decision rather than an accident.
//
// CanResume() is still load-bearing — resumeLegacyOp gates on it — and it survives
// until PR-3. Where the two disagree, Policy() is the intended value.
func TestPolicyAgreesWithCanResume(t *testing.T) {
	// CanResume()==true but ResumeDrop: jobs that checkpoint nothing AND advertise
	// dry_run:true. They cannot take ResumeRequeue until the two requeue
	// implementations stop disagreeing about params — server.resumeV2Op re-enqueues
	// with nil, under which DryRun resolves to false and a preview runs for real.
	// See todo.d/20260817-resumerequeue-two-divergent-implementations.md.
	gatedByDryRun := map[string]bool{
		"bulk-deluge-import":      true,
		"cleanup-empty-folders":   true,
		"refetch-missing-authors": true,
		"repair-missing-files":    true,
		"scan-composer-tags":      true,
	}

	jobs := maintenance.All()
	if len(jobs) == 0 {
		t.Fatal("no maintenance jobs registered; this test would pass vacuously")
	}

	resumable, gated := 0, 0
	for _, job := range jobs {
		p := job.Policy()
		dropped := p.ResumePolicy == opsregistry.ResumeDrop

		switch {
		case job.CanResume() && dropped:
			resumable++
			if !gatedByDryRun[job.ID()] {
				t.Errorf("%s: CanResume()==true but ResumePolicy==ResumeDrop, and it is "+
					"not one of the documented dry_run-gated jobs. Either give it a real "+
					"resume policy or add it to gatedByDryRun with a reason.", job.ID())
			} else {
				gated++
			}
		case job.CanResume():
			resumable++
		case !job.CanResume() && !dropped:
			t.Errorf("%s: CanResume()==false but ResumePolicy==%v; a job that cannot "+
				"resume should not be restarted or requeued", job.ID(), p.ResumePolicy)
		}
	}

	if want := 9; resumable != want {
		t.Errorf("jobs with CanResume()==true = %d, want %d", resumable, want)
	}
	if want := len(gatedByDryRun); gated != want {
		t.Errorf("dry_run-gated jobs matched = %d, want %d; an ID in gatedByDryRun "+
			"does not exist or no longer reports CanResume()==true", gated, want)
	}
}
