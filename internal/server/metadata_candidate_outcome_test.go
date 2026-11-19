// file: internal/server/metadata_candidate_outcome_test.go
// version: 1.0.0
// guid: 8d41c7e2-5a90-4b36-9f1d-2c6e0a7b3f58
// last-edited: 2026-11-19

package server

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The candidate fetch writes each book's outcome line through the env: HEAD's
// shell through logCandidateOutcome over its reporter, the native shell (no
// LogOutcome) through the env's own Log. Both carry the same line.
func TestCandidateFetchEnv_LogOutcomeFollowsTheShell(t *testing.T) {
	r := CandidateResult{}
	wantLevel, wantMsg, _ := candidateOutcomeLine(r)

	var viaEnv []string
	native := candidateFetchEnv{t7Env: t7Env{Log: func(level slog.Level, msg string, _ ...slog.Attr) {
		assert.Equal(t, wantLevel, level)
		viaEnv = append(viaEnv, msg)
	}}}
	native.logOutcome(r)
	require.Equal(t, []string{wantMsg}, viaEnv, "no LogOutcome: the env's Log gets the line")

	rep := &purgeOpReporter{}
	head := candidateFetchEnv{
		t7Env:      t7Env{Log: func(slog.Level, string, ...slog.Attr) { t.Error("HEAD's shell logs through its reporter, not env.Log") }},
		LogOutcome: func(r CandidateResult) { logCandidateOutcome(rep, r) },
	}
	head.logOutcome(r)
	require.Equal(t, []string{wantMsg}, rep.logs, "LogOutcome set: the reporter gets the line")
}
