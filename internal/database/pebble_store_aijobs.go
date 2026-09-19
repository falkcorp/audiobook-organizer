// file: internal/database/pebble_store_aijobs.go
// version: 1.2.0
// guid: 702bf788-2e84-43d9-81c3-81c3146ba7c0
// last-edited: 2026-09-19

package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// aijobWriteMu serialises every read-modify-write of an ai_jobs row. Without it
// two mutators (e.g. MarkAIJobApplyFailed from a Dispatch and
// MarkAIJobSubmitted from the reconciler) can read the same row and the later
// write silently drops the other's change. ai_jobs writes are a handful per
// poll tick, so one package-level lock costs nothing measurable.
var aijobWriteMu sync.Mutex

// CreateAIJob stores a new AIJob row and its payload blob.
func (p *PebbleStore) CreateAIJob(job AIJob, payloadJSON []byte) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("CreateAIJob marshal: %w", err)
	}
	jobKey := []byte(fmt.Sprintf("aijob:%s", job.ID))
	if err := p.db.Set(jobKey, data, pebble.Sync); err != nil {
		return fmt.Errorf("CreateAIJob set job: %w", err)
	}
	payloadKey := []byte(fmt.Sprintf("aijob_payload:%s", job.ID))
	if err := p.db.Set(payloadKey, payloadJSON, pebble.Sync); err != nil {
		return fmt.Errorf("CreateAIJob set payload: %w", err)
	}
	return nil
}

// GetAIJob retrieves a job by its ID.
func (p *PebbleStore) GetAIJob(id string) (AIJob, error) {
	jobKey := []byte(fmt.Sprintf("aijob:%s", id))
	value, closer, err := p.db.Get(jobKey)
	if err == pebble.ErrNotFound {
		return AIJob{}, fmt.Errorf("%w: %s", ErrAIJobNotFound, id)
	}
	if err != nil {
		return AIJob{}, err
	}
	defer closer.Close()
	var job AIJob
	if err := json.Unmarshal(value, &job); err != nil {
		return AIJob{}, err
	}
	return job, nil
}

// GetAIJobByBatchID retrieves a job using the OpenAI batch ID secondary index.
func (p *PebbleStore) GetAIJobByBatchID(batchID string) (AIJob, error) {
	idxKey := []byte(fmt.Sprintf("aijob_batch:%s", batchID))
	val, closer, err := p.db.Get(idxKey)
	if err == pebble.ErrNotFound {
		return AIJob{}, fmt.Errorf("%w for batch: %s", ErrAIJobNotFound, batchID)
	}
	if err != nil {
		return AIJob{}, err
	}
	jobID := string(val)
	closer.Close()
	return p.GetAIJob(jobID)
}

// GetAIJobPayload returns the raw payload JSON stored alongside the job.
func (p *PebbleStore) GetAIJobPayload(id string) ([]byte, error) {
	payloadKey := []byte(fmt.Sprintf("aijob_payload:%s", id))
	value, closer, err := p.db.Get(payloadKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("ai job payload not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), nil
}

// MarkAIJobSubmitted sets the job status to "submitted" and records the batch ID.
//
// The row and its aijob_batch: index are written in ONE pebble batch. As two
// separate writes, a kill between them left a submitted row with a batch id
// but no index, and every GetAIJobByBatchID for that batch failed forever.
// Re-calling it for a job whose index went missing rewrites both.
func (p *PebbleStore) MarkAIJobSubmitted(id, batchID string) error {
	aijobWriteMu.Lock()
	defer aijobWriteMu.Unlock()
	job, err := p.GetAIJob(id)
	if err != nil {
		return err
	}
	job.Status = "submitted"
	job.BatchID = batchID
	job.SubmittedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, nil); err != nil {
		return err
	}
	if err := b.Set([]byte(fmt.Sprintf("aijob_batch:%s", batchID)), []byte(id), nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// updateAIJob applies mutate to the stored row under aijobWriteMu and writes
// it back. It returns the updated row.
func (p *PebbleStore) updateAIJob(id string, mutate func(*AIJob)) (AIJob, error) {
	aijobWriteMu.Lock()
	defer aijobWriteMu.Unlock()
	job, err := p.GetAIJob(id)
	if err != nil {
		return AIJob{}, err
	}
	mutate(&job)
	data, err := json.Marshal(job)
	if err != nil {
		return AIJob{}, err
	}
	if err := p.db.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, pebble.Sync); err != nil {
		return AIJob{}, err
	}
	return job, nil
}

// MarkAIJobApplied records that the results were applied, with the counts
// the completion mark will need. Status is left unchanged.
func (p *PebbleStore) MarkAIJobApplied(id string, successCount, errorCount int, rowErrors []AIJobRowError) error {
	_, err := p.updateAIJob(id, func(job *AIJob) {
		job.Applied = true
		setAIJobCounts(job, successCount, errorCount, rowErrors)
	})
	return err
}

func setAIJobCounts(job *AIJob, successCount, errorCount int, rowErrors []AIJobRowError) {
	job.SuccessCount = successCount
	job.ErrorCount = errorCount
	job.RowErrors = ""
	if len(rowErrors) > 0 {
		b, _ := json.Marshal(rowErrors)
		job.RowErrors = string(b)
	}
}

// MarkAIJobCompleted sets the job status to completed/completed_with_errors and
// records success/error counts and per-row error details.
func (p *PebbleStore) MarkAIJobCompleted(id, status string, successCount, errorCount int, rowErrors []AIJobRowError) error {
	_, err := p.updateAIJob(id, func(job *AIJob) {
		job.Status = status
		setAIJobCounts(job, successCount, errorCount, rowErrors)
		job.CompletedAt = time.Now().UTC()
	})
	return err
}

// MarkAIJobFailed sets the job status to "failed" with an error message.
func (p *PebbleStore) MarkAIJobFailed(id, errMsg string) error {
	_, err := p.updateAIJob(id, func(job *AIJob) {
		job.Status = "failed"
		job.ErrorMsg = errMsg
		job.CompletedAt = time.Now().UTC()
	})
	return err
}

// MarkAIJobApplyFailed records one failed apply attempt and returns the row.
func (p *PebbleStore) MarkAIJobApplyFailed(id, errMsg string) (AIJob, error) {
	return p.updateAIJob(id, func(job *AIJob) {
		job.Status = "apply_failed"
		job.ApplyAttempts++
		job.LastApplyError = errMsg
		job.LastApplyAt = time.Now().UTC()
	})
}

// ListAIJobs returns jobs matching optional type/status filters, with
// limit/offset pagination. Results are ordered by CreatedAt descending.
func (p *PebbleStore) ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]AIJob, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("aijob:"),
		UpperBound: []byte("aijob:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []AIJob
	for iter.First(); iter.Valid(); iter.Next() {
		var job AIJob
		if err := json.Unmarshal(iter.Value(), &job); err != nil {
			continue
		}
		if typeFilter != "" && job.Type != typeFilter {
			continue
		}
		if statusFilter != "" && job.Status != statusFilter {
			continue
		}
		all = append(all, job)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	if offset >= len(all) {
		return []AIJob{}, nil
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
