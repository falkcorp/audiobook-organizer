// file: core/backup/tasks.go
// version: 1.0.0
// guid: 5a8d3f71-0b26-4c94-9e1d-2f7c6b0a4e83
// last-edited: 2026-11-12

package backup

import (
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// The kinds of 05-backup B8. Create, verify, prune and stage-restore share one
// resource and one concurrency key, so the executor never runs two of them at
// once (TestBackupTaskExclusive); the Service refuses the same overlap for a
// direct caller with ErrBackupInProgress.
const (
	KindCreate       tasks.Kind = "core.backup.create"
	KindVerify       tasks.Kind = "core.backup.verify"
	KindPrune        tasks.Kind = "core.backup.prune"
	KindStageRestore tasks.Kind = "core.backup.stage-restore"
)

// The schedule keys are backup's own (R2-7): type tasks.ScheduleConfig.
const (
	ScheduleCreateKey = "core.backup.schedules.create"
	ScheduleVerifyKey = "core.backup.schedules.verify"
)

// taskKey is the concurrency key every backup task shares.
const taskKey tasks.Key = "core.backup"

// Resource is the one exclusive backup resource (§5B.6).
func Resource() entity.Ref { return entity.Ref{Module: "core", Kind: "backup", ID: "main"} }

// CreateParams are core.backup.create's parameters.
type CreateParams struct {
	Reason             string `json:"reason,omitempty"`
	IncludeDerived     bool   `json:"include_derived,omitempty"`
	IncludeRecreatable bool   `json:"include_recreatable,omitempty"`
}

// CreateResult is what a create run reports.
type CreateResult struct {
	ArchiveID ArchiveID `json:"archive_id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Verified  bool      `json:"verified"`
}

// VerifyTaskResult is what a verify run reports.
type VerifyTaskResult struct {
	Checked   int `json:"checked"`
	Opened    int `json:"opened"`
	Rewrapped int `json:"rewrapped"`
	Failed    int `json:"failed"`
}

// StageParams are core.backup.stage-restore's parameters: the route's body.
type StageParams struct {
	ArchiveID               ArchiveID `json:"archive_id"`
	Verify                  string    `json:"verify,omitempty"` // "checksum", "entries" or "open" (default)
	AllowWeakVerify         bool      `json:"allow_weak_verify,omitempty"`
	Foreign                 bool      `json:"foreign,omitempty"`
	ReplaceConfig           bool      `json:"replace_config,omitempty"`
	AcceptUnreadableSecrets bool      `json:"accept_unreadable_secrets,omitempty"`
	Restart                 bool      `json:"restart,omitempty"`
}

// StageResult is what a stage-restore run reports.
type StageResult struct {
	Pending         PendingRestore `json:"pending"`
	RestartRequired bool           `json:"restart_required"`
}

// Empty is an empty parameter or result.
type Empty struct{}

// The four core.backup.* definitions, one constructor each. The kernel passes
// them to Registrar.Tasks at P6 once the app opts into core backup (F8-04);
// until then nothing registers them (ABO keeps its own backup task). Each is
// 05-backup B8's row over s; a nil s yields a definition whose Run is nil,
// which tasks.Problems reports ("Task has no Run"), so registration fails
// closed instead of panicking when a task starts.

// CreateTask is core.backup.create: it writes a verified archive, then applies
// retention, on the app's schedule (ScheduleCreateKey).
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 from F8-04; this package registers nothing
func CreateTask(s *Svc) tasks.Definition {
	var run func(*tasks.RC, CreateParams) (CreateResult, error)
	if s != nil {
		run = s.taskCreate
	}
	return tasks.Task(KindCreate, tasks.TaskSpec[CreateParams, CreateResult]{
		Title: "Back up", Help: "Writes a verified archive of the database, the config file and the modules' own data into the backup directory, then applies retention.",
		Effects: tasks.Writes(Resource()), Perms: manageBackups(), Resume: tasks.ResumeDrop, ConcurrencyKey: taskKey, Timeout: 12 * time.Hour,
		Schedule: tasks.ScheduleFromConfig(ScheduleCreateKey).Live(), Dedupe: tasks.DedupePolicy{AnyParams: true},
		Run: run,
	})
}

// VerifyTask is core.backup.verify: it re-verifies the newest archives, opens
// the newest, and re-signs archives made under a retiring key, on the app's
// schedule (ScheduleVerifyKey).
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 from F8-04; this package registers nothing
func VerifyTask(s *Svc) tasks.Definition {
	var run func(*tasks.RC, Empty) (VerifyTaskResult, error)
	if s != nil {
		run = s.taskVerify
	}
	return tasks.Task(KindVerify, tasks.TaskSpec[Empty, VerifyTaskResult]{
		Title: "Verify backups", Help: "Re-verifies the newest archives, opens the newest one, and re-signs archives made under a derivation key that is being retired.",
		Effects: tasks.Writes(Resource()), Perms: manageBackups(), Resume: tasks.ResumeDrop, ConcurrencyKey: taskKey, Timeout: 6 * time.Hour,
		Schedule: tasks.ScheduleFromConfig(ScheduleVerifyKey).Live(),
		Run:      run,
	})
}

// PruneTask is core.backup.prune: it applies the retention bounds.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 from F8-04; this package registers nothing
func PruneTask(s *Svc) tasks.Definition {
	var run func(*tasks.RC, Empty) (Empty, error)
	if s != nil {
		run = s.taskPrune
	}
	return tasks.Task(KindPrune, tasks.TaskSpec[Empty, Empty]{
		Title: "Prune backups", Help: "Applies the retention bounds to the backup directory, never deleting the newest verified archive or one a pending restore needs.",
		Effects: tasks.Writes(Resource()), Perms: manageBackups(), Resume: tasks.ResumeDrop, ConcurrencyKey: taskKey, Timeout: 30 * time.Minute,
		Run: run,
	})
}

// StageRestoreTask is core.backup.stage-restore: it verifies an archive and
// stages it for the next start. It is started with core:backup.restore, and
// the privileged options of StageParams are refused on this path (the service
// requires a RestoreProof only an owner-checked route can set).
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 from F8-04; this package registers nothing
func StageRestoreTask(s *Svc) tasks.Definition {
	var run func(*tasks.RC, StageParams) (StageResult, error)
	if s != nil {
		run = s.taskStage
	}
	return tasks.Task(KindStageRestore, tasks.TaskSpec[StageParams, StageResult]{
		Title: "Stage a restore", Help: "Verifies an archive and stages it for the next start. The live data is not touched until the service restarts.",
		Effects: tasks.Writes(Resource()), Perms: tasks.Permissions{Start: rbac.Need("core:backup.restore")}, Resume: tasks.ResumeDrop,
		ConcurrencyKey: taskKey, Timeout: 6 * time.Hour,
		Run: run,
	})
}

// manageBackups is the permission the create, verify and prune tasks start
// with.
func manageBackups() tasks.Permissions {
	return tasks.Permissions{Start: rbac.Need("core:system.manage")}
}

func (s *Svc) taskCreate(rc *tasks.RC, p CreateParams) (CreateResult, error) {
	ctx := WithProgress(rc.Context(), rc)
	reason := p.Reason
	if reason == "" {
		reason = "manual"
		if rc.Trigger() == tasks.TriggerSchedule {
			reason = "schedule"
		}
	}
	a, err := s.Create(ctx, CreateOptions{Reason: reason, IncludeDerived: p.IncludeDerived, IncludeRecreatable: p.IncludeRecreatable})
	// A created archive that failed its own verification is reported by the
	// error, with the archive in the result so the person sees which one.
	res := CreateResult{ArchiveID: a.ID, Name: a.Name, Size: a.Size, Verified: a.Verified != nil && a.Verified.OK}
	return res, err
}

// taskVerify re-verifies the newest VerifyKeep archives at the entries level,
// the newest at the open level, and re-signs every Own archive made under a
// derivation key that is no longer the active one (SEC2-24). Every archive is
// attempted; any failed verification fails the run, after the rest were tried.
func (s *Svc) taskVerify(rc *tasks.RC, _ Empty) (VerifyTaskResult, error) {
	ctx := WithProgress(rc.Context(), rc)
	var res VerifyTaskResult
	all, err := s.List(ctx)
	if err != nil {
		return res, err
	}
	keep := max(1, s.o.settings().VerifyKeep)
	var errs []error
	for i, a := range all { // newest first
		if i >= keep {
			break
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		level := VerifyEntries
		if i == 0 {
			level = VerifyOpen
		}
		if _, err := s.Verify(ctx, a.ID, level); err != nil {
			res.Failed++
			errs = append(errs, fmt.Errorf("%s: %w", a.ID, err))
			continue
		}
		res.Checked++
		if level == VerifyOpen {
			res.Opened++
		}
	}
	active := s.o.Keyring.ActiveDeriveID()
	for _, a := range all {
		if a.Provenance != Own || a.Manifest == nil || a.Manifest.Provenance.KeyID == active {
			continue
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if _, err := s.Rewrap(ctx, a.ID); err != nil {
			if errors.Is(err, ErrBackupInProgress) {
				return res, err
			}
			res.Failed++
			errs = append(errs, fmt.Errorf("rewrap %s: %w", a.ID, err))
			continue
		}
		res.Rewrapped++
	}
	return res, errors.Join(errs...)
}

func (s *Svc) taskPrune(rc *tasks.RC, _ Empty) (Empty, error) {
	return Empty{}, s.Prune(rc.Context())
}

func (s *Svc) taskStage(rc *tasks.RC, p StageParams) (StageResult, error) {
	level := VerifyInvalid
	switch p.Verify {
	case "":
	case "checksum":
		level = VerifyChecksum
	case "entries":
		level = VerifyEntries
	case "open":
		level = VerifyOpen
	default:
		return StageResult{}, fmt.Errorf("backup: unknown verify level %q", p.Verify)
	}
	pr, err := s.StageRestore(rc.Context(), p.ArchiveID, RestoreOptions{
		Verify: level, AllowWeakVerify: p.AllowWeakVerify, Foreign: p.Foreign, ReplaceConfig: p.ReplaceConfig,
		AcceptUnreadableSecrets: p.AcceptUnreadableSecrets, Restart: p.Restart,
	})
	return StageResult{Pending: pr, RestartRequired: true}, err
}
