// file: core/integrations/ai/jobstore.go
// version: 1.0.0
// guid: 9d4f1b27-5a63-4e80-9c2d-7b8a0e3f6c19
// last-edited: 2026-11-12

package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// ---- batch job persistence (R4-1) ---------------------------------------------

// JobState is where a stored batch job is.
type JobState uint8

// The job states.
const (
	JobPending   JobState = iota // stored; remote not yet called
	JobSubmitted                 // remote handle recorded
	JobApplying                  // results ready; the module's apply run is staged
	JobCompleted
	JobFailed
	JobCanceled
)

// Terminal reports Completed, Failed and Canceled.
func (s JobState) Terminal() bool {
	return s == JobCompleted || s == JobFailed || s == JobCanceled
}

// String is the state's stored and metric spelling.
func (s JobState) String() string {
	switch s {
	case JobPending:
		return "pending"
	case JobSubmitted:
		return "submitted"
	case JobApplying:
		return "applying"
	case JobCompleted:
		return "completed"
	case JobFailed:
		return "failed"
	case JobCanceled:
		return "canceled"
	}
	return fmt.Sprintf("job_state(%d)", uint8(s))
}

func parseJobState(s string) (JobState, bool) {
	for st := JobPending; st <= JobCanceled; st++ {
		if st.String() == s {
			return st, true
		}
	}
	return 0, false
}

// StoredJob is one persisted batch job.
type StoredJob struct {
	ID            BatchJobID
	Capability    string // catalog ID
	Module        string
	ApplyKind     string // "<module>.ai.batch-apply.<name>"
	Endpoint      integrations.Ref
	Handle        BatchHandle
	State         JobState
	ApplyAttempts int
	Payload       []byte // the caller's SubmitBatch payload; returned by Get, not by Open
	Created       time.Time
	Updated       time.Time
}

// BatchJob is the router's view of one job: the stored job without its payload.
type BatchJob = StoredJob

// JobStore persists batch jobs (SDK surface). Core ships its one
// implementation, NativeJobStore. Writes stage into the caller's batch, so a
// job's state change commits with the poll step, apply run or cancel that
// caused it.
//
// EVERY write is checked at commit (R6-1, R5-4, DATA5-1, DATA6-1). Create and
// Transition are the only writing methods, so no write is unconditioned: there
// is no progress tick, count, attempt, error-text or timestamp update outside
// a Transition. Create expects the job absent. Transition reads the stored job
// through b.GetForUpdate, returns ErrJobConflict when its State is not from,
// and otherwise stages next, derived from that read; a write that keeps the
// state (an apply run's raised attempt count) is a Transition with from ==
// next.State and the same precondition. A concurrent commit to any key it read
// fails b with storage.ErrConflict, so a poll, an apply run and an HTTP cancel
// racing on one job never both commit, and a same-state write can never revert
// a cancel. A failed write is DROPPED: nothing of its batch applies, and its
// writer re-reads and decides again; no caller re-stages a row derived from
// the stale read, and no implementation retries one internally. None relies on
// a lock held between calls for its correctness: the job families c/ai/jobs/
// and c/ai/ix/open/ are Serialized, so storage refuses a blind write
// (ErrBlindWrite).
//
// b must be an indexed batch over the store's own keyspace
// (Keyspace.NewIndexedBatch), so a second Transition of one job in one batch
// sees the first.
type JobStore interface {
	Get(ctx context.Context, id BatchJobID) (StoredJob, error)
	Open(ctx context.Context) iter.Seq2[StoredJob, error]                                                 // non-terminal jobs, payload omitted
	Create(ctx context.Context, b *storage.Batch, j StoredJob) error                                      // j.State == JobPending; payload included
	Transition(ctx context.Context, b *storage.Batch, id BatchJobID, from JobState, next StoredJob) error // ErrJobConflict
}

// ---- the native store ------------------------------------------------------------

// Owner is the storage owner of the c/ai/ keyspace.
const Owner storage.Owner = "core/ai"

// The first key segments of the three families (05-ai section 4 "Native
// store"): c/ai/jobs/<id>, c/ai/payloads/<id>, c/ai/ix/open/<id>; and the
// notes an import records beside a job it had to fail.
const (
	jobsSeg     = "jobs"
	payloadsSeg = "payloads"
	ixSeg       = "ix"
	openSeg     = "open"
	notesSeg    = "notes"
)

// StorageSpec is the c/ai/ spec the kernel registers for Owner: a core
// keyspace whose job rows and open index are Serialized, so every write to one
// carries an Expect (05-storage R6-1) and two writers of one job conflict
// instead of losing a write. Payloads are written once, in the Create batch,
// beside the job row's own Expect, and never rewritten.
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{string(keys.Prefix(keys.Str(jobsSeg))), string(openPrefix())}}
}

func openPrefix() []byte { return keys.Prefix(keys.Str(ixSeg), keys.Str(openSeg)) }

func jobKey(id BatchJobID) []byte     { return keys.Join(keys.Str(jobsSeg), keys.Str(string(id))) }
func payloadKey(id BatchJobID) []byte { return keys.Join(keys.Str(payloadsSeg), keys.Str(string(id))) }
func notesKey(id BatchJobID) []byte   { return keys.Join(keys.Str(notesSeg), keys.Str(string(id))) }
func openKey(id BatchJobID) []byte {
	return keys.Join(keys.Str(ixSeg), keys.Str(openSeg), keys.Str(string(id)))
}

// jobRow is the stored form of a job without its payload:
// codec.JSON[jobRow](1).
type jobRow struct {
	ID            string    `json:"id"`
	Capability    string    `json:"capability"`
	Module        string    `json:"module"`
	ApplyKind     string    `json:"apply_kind"`
	EndpointKind  string    `json:"endpoint_kind"`
	EndpointID    string    `json:"endpoint"`
	Handle        string    `json:"handle,omitempty"`
	State         string    `json:"state"`
	ApplyAttempts int       `json:"apply_attempts"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
}

func rowCodec() codec.Codec[jobRow] { return codec.JSON[jobRow](1) }

func encodeJob(j StoredJob) ([]byte, error) {
	return rowCodec().Encode(jobRow{
		ID: string(j.ID), Capability: j.Capability, Module: j.Module, ApplyKind: j.ApplyKind,
		EndpointKind: string(j.Endpoint.Kind), EndpointID: string(j.Endpoint.Instance),
		Handle: string(j.Handle), State: j.State.String(), ApplyAttempts: j.ApplyAttempts,
		Created: j.Created, Updated: j.Updated,
	})
}

func decodeJob(raw []byte) (StoredJob, error) {
	r, err := rowCodec().Decode(raw)
	if err != nil {
		return StoredJob{}, fmt.Errorf("ai: decode a job row: %w", err)
	}
	st, ok := parseJobState(r.State)
	if !ok {
		return StoredJob{}, fmt.Errorf("ai: job %q has the unknown state %q", r.ID, r.State)
	}
	return StoredJob{
		ID: BatchJobID(r.ID), Capability: r.Capability, Module: r.Module, ApplyKind: r.ApplyKind,
		Endpoint: integrations.Ref{Kind: integrations.Kind(r.EndpointKind), Instance: integrations.InstanceID(r.EndpointID)},
		Handle:   BatchHandle(r.Handle), State: st, ApplyAttempts: r.ApplyAttempts, Created: r.Created, Updated: r.Updated,
	}, nil
}

// legalTransition is the job state graph: a non-terminal job may keep its state
// (an endpoint change, an attempt count) or move forward; a terminal one never
// moves.
func legalTransition(from, to JobState) bool {
	if from.Terminal() {
		return false
	}
	switch from {
	case JobPending:
		return to == JobPending || to == JobSubmitted || to == JobFailed || to == JobCanceled
	case JobSubmitted:
		return to == JobSubmitted || to == JobApplying || to == JobFailed || to == JobCanceled
	case JobApplying:
		return to == JobApplying || to == JobCompleted || to == JobFailed || to == JobCanceled
	}
	return false
}

// nativeStore is NativeJobStore.
type nativeStore struct{ ks storage.Keyspace }

// NativeJobStore is core's implementation over its own keyspace c/ai/ (ks is
// the keyspace of Owner, opened with StorageSpec). It holds no cache and no
// lock: every write stages into the caller's batch with an Expect on the
// committed value it derives from.
func NativeJobStore(ks storage.Keyspace) JobStore { return nativeStore{ks: ks} }

// Get returns the job with its payload; ErrJobNotFound when absent. A failed
// snapshot Close is the error when nothing else failed.
func (s nativeStore) Get(ctx context.Context, id BatchJobID) (out StoredJob, err error) {
	snap, err := s.ks.Snapshot()
	if err != nil {
		return StoredJob{}, fmt.Errorf("ai: read job %s: %w", id, err)
	}
	defer func() {
		if cerr := snap.Close(); cerr != nil && err == nil {
			out, err = StoredJob{}, fmt.Errorf("ai: read job %s: close snapshot: %w", id, cerr)
		}
	}()
	raw, err := snap.Get(ctx, jobKey(id))
	if errors.Is(err, storage.ErrNotFound) {
		return StoredJob{}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if err != nil {
		return StoredJob{}, fmt.Errorf("ai: read job %s: %w", id, err)
	}
	j, err := decodeJob(raw)
	if err != nil {
		return StoredJob{}, err
	}
	payload, err := snap.Get(ctx, payloadKey(id))
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return StoredJob{}, fmt.Errorf("ai: read job %s payload: %w", id, err)
	default:
		j.Payload = payload
	}
	return j, nil
}

// Open yields the non-terminal jobs in ID order, payload omitted, read from one
// snapshot so a job is never listed twice or half moved. The snapshot is closed
// before the first job is yielded: a poll that calls a provider for each job
// holds no storage lease while it does, and a failed Close still has a consumer
// to be reported to. An index row whose job row is missing, or a row that
// cannot be decoded, ends the sequence with an error after the jobs before it:
// a poll that cannot read a job must not treat it as absent.
func (s nativeStore) Open(ctx context.Context) iter.Seq2[StoredJob, error] {
	return func(yield func(StoredJob, error) bool) {
		jobs, err := s.readOpen(ctx)
		for _, j := range jobs {
			if !yield(j, nil) {
				return
			}
		}
		if err != nil {
			yield(StoredJob{}, err)
		}
	}
}

// readOpen reads the open jobs from one snapshot and closes it. It returns the
// jobs read before the first failure together with that failure; a failed Close
// is the error when nothing else failed.
func (s nativeStore) readOpen(ctx context.Context) (jobs []StoredJob, err error) {
	snap, err := s.ks.Snapshot()
	if err != nil {
		return nil, fmt.Errorf("ai: list open jobs: %w", err)
	}
	defer func() {
		if cerr := snap.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("ai: list open jobs: close snapshot: %w", cerr)
		}
	}()
	prefix := openPrefix()
	for kv, serr := range snap.Scan(ctx, storage.ScanOptions{Start: prefix, End: keys.PrefixEnd(prefix)}) {
		if serr != nil {
			return jobs, fmt.Errorf("ai: list open jobs: %w", serr)
		}
		segs, kerr := keys.Split(kv.Key)
		if kerr != nil || len(segs) != 3 {
			return jobs, fmt.Errorf("ai: open index key %q is not an index row", kv.Key)
		}
		id := BatchJobID(segs[2])
		raw, gerr := snap.Get(ctx, jobKey(id))
		if gerr != nil {
			return jobs, fmt.Errorf("ai: open index row of job %s has no readable job: %w", id, gerr)
		}
		j, derr := decodeJob(raw)
		if derr != nil {
			return jobs, derr
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// Create stages j, its payload and its open-index row into b, expecting the
// job (and its index row) absent.
func (s nativeStore) Create(ctx context.Context, b *storage.Batch, j StoredJob) error {
	if b == nil {
		return errors.New("ai: Create needs the caller's batch")
	}
	if j.ID == "" {
		return errors.New("ai: Create needs a job ID")
	}
	if j.State != JobPending {
		return fmt.Errorf("ai: Create takes a %s job, not %s", JobPending, j.State)
	}
	return s.stageNew(ctx, b, j)
}

// stageNew stages a job absent from the store: the shared body of Create and
// the importer's write. It reads the job row with GetForUpdate, which records
// the absent Expect, then stages the job.
func (s nativeStore) stageNew(ctx context.Context, b *storage.Batch, j StoredJob) error {
	b.Bind(s.ks)
	switch _, err := b.GetForUpdate(ctx, s.ks, jobKey(j.ID)); {
	case err == nil:
		return fmt.Errorf("%w: job %s already exists", ErrJobConflict, j.ID)
	case !errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("ai: read job %s: %w", j.ID, err)
	}
	return s.stageNewAfterRead(ctx, b, j)
}

// Transition reads the stored job with b.GetForUpdate and, when its state is
// from, stages next over it. The job's identity (ID, capability, module, apply
// kind, creation time) and payload are the stored ones; next carries the
// endpoint, handle, state, attempt count and update time. A job that is not in
// from is ErrJobConflict and nothing is staged. A move that changes whether
// the job is open deletes the open-index row in the same batch.
func (s nativeStore) Transition(ctx context.Context, b *storage.Batch, id BatchJobID, from JobState, next StoredJob) error {
	if b == nil {
		return errors.New("ai: Transition needs the caller's batch")
	}
	b.Bind(s.ks)
	raw, err := b.GetForUpdate(ctx, s.ks, jobKey(id))
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("ai: read job %s: %w", id, err)
	}
	cur, err := decodeJob(raw)
	if err != nil {
		return err
	}
	if cur.State != from {
		return fmt.Errorf("%w: job %s is %s, the write expected %s", ErrJobConflict, id, cur.State, from)
	}
	if !legalTransition(from, next.State) {
		return fmt.Errorf("ai: job %s may not move from %s to %s", id, from, next.State)
	}
	next.ID, next.Capability, next.Module, next.ApplyKind, next.Created = cur.ID, cur.Capability, cur.Module, cur.ApplyKind, cur.Created
	row, err := encodeJob(next)
	if err != nil {
		return err
	}
	w := b.In(s.ks)
	if next.State.Terminal() {
		switch _, err := b.GetForUpdate(ctx, s.ks, openKey(id)); {
		case err == nil:
			if err := w.Delete(openKey(id)); err != nil {
				return fmt.Errorf("ai: stage the open index delete of job %s: %w", id, err)
			}
		case !errors.Is(err, storage.ErrNotFound):
			return fmt.Errorf("ai: read the open index row of job %s: %w", id, err)
		}
	}
	if err := w.Set(jobKey(id), row); err != nil {
		return fmt.Errorf("ai: stage job %s: %w", id, err)
	}
	return nil
}

// ---- the importer ---------------------------------------------------------------

// Importer is what an app's cutover step calls to write its pre-core batch
// jobs (R2 step M6, section 10). The kernel builds it over c/ai/ and hands it
// to the step in module.Importers. ImportJob writes j in any state, terminal
// ones included, with its payload and its open-index row when it is
// non-terminal, expecting the job absent, and stages everything into b, so the
// job commits with the step's deletion of the rows it came from. j.Capability
// must be a catalog ID and j.ApplyKind a registered apply kind; a job that
// names neither is imported as JobFailed with its reason recorded, never
// dropped.
type Importer interface {
	ImportJob(ctx context.Context, b *storage.Batch, j StoredJob) error
}

// CatalogView is what an importer asks of the router's catalog: whether a
// capability ID is registered and whether an apply kind has a registered
// BatchApplyTask. *Router implements it.
type CatalogView interface {
	HasCapability(id string) bool
	HasApplyKind(kind string) bool
}

// ImporterOption configures NativeImporter.
type ImporterOption func(*nativeImporter)

// ImportWithCatalog makes the importer check each job's capability and apply
// kind against cv (the router's catalog, frozen before the cutover step runs).
// A job naming an unregistered capability or apply kind is written as
// JobFailed, with the reason in the job's note row. Without a catalog the
// importer writes every job as given: the caller vouches for its IDs.
func ImportWithCatalog(cv CatalogView) ImporterOption {
	return func(i *nativeImporter) { i.cv = cv }
}

type nativeImporter struct {
	store nativeStore
	cv    CatalogView
}

// NativeImporter is core's Importer over c/ai/ (ks is the keyspace of Owner).
func NativeImporter(ks storage.Keyspace, opts ...ImporterOption) Importer {
	i := &nativeImporter{store: nativeStore{ks: ks}}
	for _, o := range opts {
		o(i)
	}
	return i
}

// ImportJob stages j into b. A job already stored with exactly this content is
// a no-op (a re-run of a step that committed); any other stored job is
// storage.ErrImportConflict.
func (i *nativeImporter) ImportJob(ctx context.Context, b *storage.Batch, j StoredJob) error {
	if b == nil {
		return errors.New("ai: ImportJob needs the step's batch")
	}
	if j.ID == "" {
		return errors.New("ai: ImportJob needs a job ID")
	}
	note := ""
	if i.cv != nil {
		switch {
		case !i.cv.HasCapability(j.Capability):
			note = fmt.Sprintf("imported as failed: capability %q is not in the catalog", j.Capability)
		case !i.cv.HasApplyKind(j.ApplyKind):
			note = fmt.Sprintf("imported as failed: apply kind %q has no registered BatchApplyTask", j.ApplyKind)
		}
		if note != "" {
			j.State = JobFailed
		}
	}
	b.Bind(i.store.ks)
	raw, err := b.GetForUpdate(ctx, i.store.ks, jobKey(j.ID))
	switch {
	case err == nil:
		want, eerr := encodeJob(j)
		if eerr != nil {
			return eerr
		}
		if bytes.Equal(raw, want) {
			return nil
		}
		return fmt.Errorf("%w: batch job %s already has a row with other content", storage.ErrImportConflict, j.ID)
	case !errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("ai: read job %s: %w", j.ID, err)
	}
	if err := i.store.stageNewAfterRead(ctx, b, j); err != nil {
		return err
	}
	if note != "" {
		if err := b.In(i.store.ks).Set(notesKey(j.ID), []byte(note)); err != nil {
			return fmt.Errorf("ai: stage the import note of job %s: %w", j.ID, err)
		}
	}
	return nil
}

// stageNewAfterRead is stageNew for a caller that has already read the job row
// with GetForUpdate and found it absent (the importer): the Expect is recorded.
func (s nativeStore) stageNewAfterRead(ctx context.Context, b *storage.Batch, j StoredJob) error {
	row, err := encodeJob(j)
	if err != nil {
		return err
	}
	w := b.In(s.ks)
	if !j.State.Terminal() {
		switch _, err := b.GetForUpdate(ctx, s.ks, openKey(j.ID)); {
		case err == nil:
			return fmt.Errorf("%w: the open index already holds job %s", ErrJobConflict, j.ID)
		case !errors.Is(err, storage.ErrNotFound):
			return fmt.Errorf("ai: read the open index row of job %s: %w", j.ID, err)
		}
		if err := w.Set(openKey(j.ID), []byte{}); err != nil {
			return fmt.Errorf("ai: stage the open index row of job %s: %w", j.ID, err)
		}
	}
	if err := w.Set(jobKey(j.ID), row); err != nil {
		return fmt.Errorf("ai: stage job %s: %w", j.ID, err)
	}
	if len(j.Payload) > 0 {
		if err := w.Set(payloadKey(j.ID), j.Payload); err != nil {
			return fmt.Errorf("ai: stage the payload of job %s: %w", j.ID, err)
		}
	}
	return nil
}
