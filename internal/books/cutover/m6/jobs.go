// file: internal/books/cutover/m6/jobs.go
// version: 1.0.1
// guid: 8f23f612-3a1b-415d-8bc4-11836c3c671e
// last-edited: 2026-11-19

package m6

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/storage"
)

// Outcomes of the two rows a job brings with it.
const (
	OutcomeJobPayload = "job-payload-folded"
	OutcomeJobIndex   = "job-index-dropped"
)

// aiJob is HEAD's aijob: row (internal/books/store/ai_jobs_types.go AIJob),
// frozen here: this package cannot import the store whose rows it retires, and
// HEAD's row never changes again. Only the fields a native job can hold are
// read; the rest are listed in jobLossy.
type aiJob struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	BatchID       string    `json:"batch_id"`
	Status        string    `json:"status"`
	SubmittedAt   time.Time `json:"submitted_at"`
	CompletedAt   time.Time `json:"completed_at"`
	CreatedAt     time.Time `json:"created_at"`
	ApplyAttempts int       `json:"apply_attempts"`
	LastApplyAt   time.Time `json:"last_apply_at"`
}

// jobLossy are the aijob fields StoredJob has no field for (05-ai section 10
// maps the row onto StoredJob, which keeps identity, state, handle, attempts,
// payload and times): custom_id_prefix, item_count, success_count, error_count,
// row_errors, error_msg, applied, last_apply_error. A terminal job's counts and
// error text are history that does not survive the cutover; an open job's are
// recomputed by the native poll and apply, which HEAD's replay does too.

// jobState is the table of 05-ai section 10: HEAD's status to the native state.
func jobState(status string) (ai.JobState, bool) {
	switch status {
	case "pending":
		return ai.JobPending, true
	case "submitted":
		return ai.JobSubmitted, true
	case "apply_failed":
		return ai.JobApplying, true
	case "completed", "completed_with_errors":
		return ai.JobCompleted, true
	case "failed", "expired":
		return ai.JobFailed, true
	}
	return 0, false
}

// job migrates the aijob: row kv and, in the same chunk, the aijob_payload: row
// and the aijob_batch: index row it owns, which sort after it and are pulled
// forward. A row that cannot be converted safely is quarantined, never rewritten.
func (m *Migration) job(ctx context.Context, c *chunk, kv storage.KV) error {
	key := string(kv.Key)
	id := key[len(PrefixAIJob):]
	var row aiJob
	if err := json.Unmarshal(kv.Value, &row); err != nil {
		return c.quarantine(key, kv.Value, reasonJobUndecodable)
	}
	if row.ID != "" && row.ID != id {
		return c.quarantine(key, kv.Value, reasonJobKey)
	}
	state, ok := jobState(row.Status)
	if !ok {
		return c.quarantine(key, kv.Value, reasonJobState)
	}
	payloadKey := PrefixAIJobPayload + id
	payload, err := m.get(ctx, payloadKey)
	if err != nil {
		return err
	}
	// The batch index row is deleted with the job only when it names this job: a
	// row under the same batch ID that names another is another job's, or an
	// orphan, and is quarantined when the shard reaches it.
	var batchKey string
	if row.BatchID != "" {
		batchKey = PrefixAIJobBatch + row.BatchID
		raw, err := m.get(ctx, batchKey)
		if err != nil {
			return err
		}
		if raw == nil || string(raw) != id {
			batchKey = ""
		}
	}

	t, known := jobTypes[row.Type]
	if !known {
		t = unknownJobType(row.Type)
		// No catalog capability: JobFailed whatever the HEAD status, so the job is
		// kept as history and never polled (05-ai section 10).
		state = ai.JobFailed
	}
	updated := row.CreatedAt
	for _, ts := range []time.Time{row.SubmittedAt, row.LastApplyAt, row.CompletedAt} {
		if ts.After(updated) {
			updated = ts
		}
	}
	job := ai.StoredJob{
		ID:            ai.BatchJobID(id),
		Capability:    t.Capability,
		Module:        Owner,
		ApplyKind:     t.ApplyKind,
		Endpoint:      integrations.Ref{Kind: integrations.KindAI, Instance: "openai"},
		Handle:        ai.BatchHandle(row.BatchID),
		State:         state,
		ApplyAttempts: row.ApplyAttempts,
		Payload:       payload,
		Created:       row.CreatedAt,
		Updated:       updated,
	}
	m.report(Entry{Kind: "job", ID: id, Detail: state.String()})
	switch err := m.cfg.AI.ImportJob(ctx, c.batch(), job); {
	case err == nil:
	case errors.Is(err, storage.ErrImportConflict):
		return c.quarantine(key, kv.Value, reasonJobConflict)
	default:
		return errors.Join(errors.New("m6: import job "+id), err)
	}
	outcome := OutcomeJob
	if state == ai.JobFailed && (!known || row.Status != "failed" && row.Status != "expired") {
		outcome = OutcomeJobFailed
	}
	if err := c.consume(key, outcome); err != nil {
		return err
	}
	if payload != nil {
		if err := c.consume(payloadKey, OutcomeJobPayload); err != nil {
			return err
		}
	}
	if batchKey != "" {
		if err := c.consume(batchKey, OutcomeJobIndex); err != nil {
			return err
		}
	}
	return nil
}
