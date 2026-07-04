// file: internal/database/pebble_store_aijobs.go
// version: 1.0.0
// guid: 702bf788-2e84-43d9-81c3-81c3146ba7c0
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

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
		return AIJob{}, fmt.Errorf("ai job not found: %s", id)
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
		return AIJob{}, fmt.Errorf("ai job not found for batch: %s", batchID)
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
func (p *PebbleStore) MarkAIJobSubmitted(id, batchID string) error {
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
	if err := p.db.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, pebble.Sync); err != nil {
		return err
	}
	// Write secondary index: batch_id → job_id
	return p.db.Set([]byte(fmt.Sprintf("aijob_batch:%s", batchID)), []byte(id), pebble.Sync)
}

// MarkAIJobCompleted sets the job status to completed/completed_with_errors and
// records success/error counts and per-row error details.
func (p *PebbleStore) MarkAIJobCompleted(id, status string, successCount, errorCount int, rowErrors []AIJobRowError) error {
	job, err := p.GetAIJob(id)
	if err != nil {
		return err
	}
	job.Status = status
	job.SuccessCount = successCount
	job.ErrorCount = errorCount
	job.CompletedAt = time.Now().UTC()
	if len(rowErrors) > 0 {
		b, _ := json.Marshal(rowErrors)
		job.RowErrors = string(b)
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, pebble.Sync)
}

// MarkAIJobFailed sets the job status to "failed" with an error message.
func (p *PebbleStore) MarkAIJobFailed(id, errMsg string) error {
	job, err := p.GetAIJob(id)
	if err != nil {
		return err
	}
	job.Status = "failed"
	job.ErrorMsg = errMsg
	job.CompletedAt = time.Now().UTC()
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(fmt.Sprintf("aijob:%s", id)), data, pebble.Sync)
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
