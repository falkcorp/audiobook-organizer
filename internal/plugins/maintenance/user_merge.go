// file: internal/plugins/maintenance/user_merge.go
// version: 1.2.0
// guid: 6c2f8a14-93d7-4b50-a1e6-d0b45f7c9823
// last-edited: 2026-11-19

// maintenance.user-merge runs the admin "merge user B into user A" tool
// (internal/usermerge) as a background operation: the apply, the resume of an
// interrupted apply, and the undo from a journal.
//
// It is PREVIEW BY DEFAULT: `{}` and `{"dry_run":true}` write nothing.
//
// A live run is refused unless it redeems a usermerge.Grant. The dedicated
// routes (POST /api/v1/users/:id/merge-into/:target/apply and
// /users/merges/:jid/{resume,undo}) mint the grant after their own checks (the
// credential-route guard, the owner gate, an active admin, actor is not the
// absorbed account) and enqueue this op with the grant token. The generic
// POST /api/v1/operations/v2 enqueues any registered definition with any
// params and none of those checks, so without the grant it would be a way
// around them; with it, an op enqueued there carries no valid token and writes
// nothing. The grant is bound to the actor recorded on the op row, so a token
// copied out of GET /operations into an op of another user's is refused too.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// UserMergeOpID is the operation definition id.
const UserMergeOpID = usermerge.OpDefID

// userMergeParams is what maintenance.user-merge decodes. Every field but the
// mode switch is part of the grant's binding on a live run.
type userMergeParams struct {
	// DryRun (dry_run) and DryRunCamel (dryRun) are one switch, resolved by
	// opmode.ResolveDryRun: omitted means preview.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// Action is apply (default), resume or undo.
	Action     string `json:"action,omitempty"`
	SurvivorID string `json:"survivor_id,omitempty"`
	AbsorbedID string `json:"absorbed_id,omitempty"`
	// Digest is the digest of the preview the admin confirmed (apply only).
	Digest string `json:"digest,omitempty"`
	// JournalID names the journal a resume or undo works on.
	JournalID                string `json:"journal_id,omitempty"`
	AcknowledgeOwnerIdentity bool   `json:"acknowledge_owner_identity,omitempty"`
	// Grant is the single-use token the merge routes mint.
	Grant string `json:"grant,omitempty"`
}

// userMergeHelp is the def's Description, which the native kind carries as its
// Help.
const userMergeHelp = "Admin tool behind Users > Merge into. Preview by default ({} writes nothing); a live run (dry_run=false) only runs when started from the Users page's own merge request, which mints the single-use grant it redeems. Applies a previewed merge, resumes an interrupted one, or undoes one from its journal."

// userMergeKind is rule R3's kind for the def below.
const userMergeKind = tasks.Kind("books.maintenance.user-merge")

func (p *Plugin) userMergeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:      UserMergeOpID,
		Preview: sdk.PreviewSpec{Params: userMergeParams{}},
		// LivenessNone plus ProgressTimeout: the merge holds process-local locks
		// (the merge serialization lock and per-book stripes), so the watchdog
		// must not reap it while it runs, and it never runs isolated.
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 30 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Merge one user into another",
		Description:     userMergeHelp,
		// A resumed run would merge without the click that started it; an
		// interrupted merge is resumed from the Users page.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh,
		ConcurrencyKey:  UserMergeOpID,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runUserMerge,
	}
}

// userMergeStore is the reads the run makes: the actor's admin check, the plan a
// preview reports and the journal a resume or undo preview reports.
type userMergeStore interface {
	ActorIsAdmin(id string) (bool, error)
	BuildPlan(ctx context.Context, req usermerge.PlanRequest) (*usermerge.Plan, error)
	LoadJournal(id string) (*usermerge.Journal, error)
}

// userMergeWriter is the three writes: apply a confirmed merge, resume an
// interrupted one, undo one from its journal. The names carry "UserMerge" so
// each has its own row in the shared table (Apply, Resume and Undo alone would
// share a row with every other writer that has a method of that name).
type userMergeWriter interface {
	ApplyUserMerge(ctx context.Context, req usermerge.ApplyRequest, prog usermerge.Progress) (*usermerge.Journal, error)
	ResumeUserMerge(ctx context.Context, journalID string, prog usermerge.Progress) (*usermerge.Journal, error)
	UndoUserMerge(ctx context.Context, req usermerge.UndoRequest, prog usermerge.Progress) (*usermerge.Journal, error)
}

// userMergeHeadWriter is HEAD's writer: the user-merge service itself.
type userMergeHeadWriter struct{ svc *usermerge.Service }

func (w userMergeHeadWriter) ApplyUserMerge(ctx context.Context, req usermerge.ApplyRequest, prog usermerge.Progress) (*usermerge.Journal, error) {
	return w.svc.Apply(ctx, req, prog)
}

func (w userMergeHeadWriter) ResumeUserMerge(ctx context.Context, id string, prog usermerge.Progress) (*usermerge.Journal, error) {
	return w.svc.Resume(ctx, id, prog)
}

func (w userMergeHeadWriter) UndoUserMerge(ctx context.Context, req usermerge.UndoRequest, prog usermerge.Progress) (*usermerge.Journal, error) {
	return w.svc.Undo(ctx, req, prog)
}

// userMergeNative is the native kind. Preview is HEAD's default (nothing is
// written); a Live run still redeems the single-use grant the Users page minted
// for the run's own actor. The actor is the run's (rc.Actor), where HEAD read it
// off the operation row, and a resumed run is not fresh (HEAD also counted
// manual retries, which the kernel has no separate count for: a seam).
func (p *Plugin) userMergeNative(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g7Common(userMergeKind, sched)
	c.Title = "Merge one user into another"
	c.Help = userMergeHelp
	// Apply, resume and undo write the accounts' listening state, preferences and
	// the merge journal. HEAD's closest resource is user_state; the user rows,
	// credentials and the journal have no resource of their own yet (seam).
	c.Effects = tasks.Writes(t6g7Res(t6WriterResources["ApplyUserMerge"]...)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityHigh
	c.ConcurrencyKey = tasks.Key("books." + UserMergeOpID)
	c.Timeout = 2 * time.Hour
	c.ProgressTimeout = 30 * time.Minute
	return tasks.Task(userMergeKind, tasks.TaskSpec[json.RawMessage, json.RawMessage]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (json.RawMessage, error) {
			params, err := t6g7DecodeNative[userMergeParams](userMergeKind, raw)
			if err != nil {
				return nil, err
			}
			if err := t6g7NeedDep(userMergeKind, "Store", deps.Store != nil); err != nil {
				return nil, err
			}
			live := rc.Mode() == tasks.ModeLive
			var result json.RawMessage
			base := t6g7NativeEnv(rc)
			env := userMergeEnv{
				Store:  deps.Store,
				Grants: usermerge.DefaultGrants,
				Actor: func() (string, bool, error) {
					return rc.Actor().UserID, !rc.Resumed(), nil
				},
				SetResult: func(v any) error {
					b, err := json.Marshal(v)
					if err != nil {
						return err
					}
					result = b
					return nil
				},
				Progress: base.Progress,
				Log:      func(level slog.Level, msg string) { base.Log(level, msg) },
			}
			if live {
				w, err := t6g7NewWriter(userMergeKind, deps, rc)
				if err != nil {
					return nil, err
				}
				env.Writer = w
			}
			err = userMergeCore(rc.Context(), !live, params, env)
			return result, err
		},
	})
}

// userMergeEnv is what a run needs, split out so the logic is testable without
// a registry.
type userMergeEnv struct {
	Store userMergeStore
	// Writer is nil in a preview (and in a native Preview run).
	Writer userMergeWriter
	Grants *usermerge.Grants
	// Actor is the user and run history of the op row this run belongs to.
	Actor func() (userID string, fresh bool, err error)
	// SetResult records the run's result data on the op row. Operation rows are
	// readable by anyone with library.view, so only the redacted forms
	// (usermerge.PlanSummary, usermerge.JournalResult) are ever passed: the full
	// plan and journal name accounts, emails and rows, and are served only by the
	// admin-gated Users merge routes.
	SetResult func(any) error
	// Progress reports progress and refreshes liveness.
	Progress func(done, total int, msg string)
	Log      func(level slog.Level, msg string)
}

func (p *Plugin) runUserMerge(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params userMergeParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	svc := p.deps.UserMerge()
	if svc == nil {
		return errors.New("user-merge: this store cannot merge users")
	}
	opID := opsregistry.ReporterOpID(reporter)
	env := userMergeEnv{
		Store:  svc,
		Writer: userMergeHeadWriter{svc: svc},
		Grants: usermerge.DefaultGrants,
		Actor: func() (string, bool, error) {
			reader, ok := p.deps.OperationQueueStore().(interface {
				GetOperationV2(id string) (*database.OperationV2Row, error)
			})
			if !ok || opID == "" {
				return "", false, errors.New("operation store cannot read this run's row")
			}
			row, err := reader.GetOperationV2(opID)
			if err != nil || row == nil || row.ActorUserID == nil {
				return "", false, err
			}
			return *row.ActorUserID, row.ManualRetryCount == 0 && row.ResumeCount == 0, nil
		},
		SetResult: func(v any) error { return opsregistry.ReporterSetResult(reporter, v) },
		Progress: func(done, total int, msg string) {
			_ = reporter.UpdateProgress(done, total, msg)
			opsregistry.TouchLiveness(reporter)
		},
		Log: func(level slog.Level, msg string) { _ = reporter.Log(level, msg) },
	}
	return runUserMerge(ctx, params, env)
}

// runUserMerge is HEAD's entry: the mode comes from the params' dry-run switch.
func runUserMerge(ctx context.Context, params userMergeParams, env userMergeEnv) error {
	dry, err := opmode.ResolveDryRun(UserMergeOpID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	return userMergeCore(ctx, dry, params, env)
}

// userMergeCore is the body of both shells: dry is the mode, decided by the
// shell (HEAD: the params' switch; native: the start request).
func userMergeCore(ctx context.Context, dry bool, params userMergeParams, env userMergeEnv) error {
	action := usermerge.Action(params.Action)
	if params.Action == "" {
		action = usermerge.ActionApply
	}
	if !action.Valid() {
		return fmt.Errorf("%s: unknown action %q (apply, resume or undo)", UserMergeOpID, params.Action)
	}
	// Both a preview and a live run belong to an active admin: a preview reads
	// two accounts' data and sign-ins, which is not for every operations reader.
	actor, fresh, err := env.Actor()
	if err != nil {
		return fmt.Errorf("%s: %w", UserMergeOpID, err)
	}
	isAdmin, err := env.Store.ActorIsAdmin(actor)
	if err != nil {
		return err
	}
	if !isAdmin {
		return fmt.Errorf("%s: the run's actor is not an active admin", UserMergeOpID)
	}
	if dry {
		return previewUserMerge(ctx, params, action, actor, env)
	}

	// Live. The grant is consumed first, so a refused run leaves no usable token.
	want := usermerge.Grant{Action: action, SurvivorID: params.SurvivorID, AbsorbedID: params.AbsorbedID,
		Digest: params.Digest, JournalID: params.JournalID, AcknowledgeOwnerIdentity: params.AcknowledgeOwnerIdentity}
	if action != usermerge.ActionApply {
		want.SurvivorID, want.AbsorbedID, want.Digest = "", "", ""
	}
	gr, err := env.Grants.Redeem(params.Grant, actor, want)
	if err != nil {
		return fmt.Errorf("%s: %w", UserMergeOpID, err)
	}
	if !fresh {
		// A run retried in place keeps its actor whoever clicks Retry.
		return fmt.Errorf("%s: a merge run is never retried in place; start it again from the Users page", UserMergeOpID)
	}
	prog := usermerge.Progress(env.Progress)
	var j *usermerge.Journal
	switch action {
	case usermerge.ActionApply:
		j, err = env.Writer.ApplyUserMerge(ctx, usermerge.ApplyRequest{ActorID: actor, SurvivorID: params.SurvivorID,
			AbsorbedID: params.AbsorbedID, Digest: params.Digest, AcknowledgeOwnerIdentity: params.AcknowledgeOwnerIdentity,
			Principal: gr.Principal}, prog)
	case usermerge.ActionResume:
		j, err = env.Writer.ResumeUserMerge(ctx, params.JournalID, prog)
	case usermerge.ActionUndo:
		j, err = env.Writer.UndoUserMerge(ctx, usermerge.UndoRequest{JournalID: params.JournalID}, prog)
	}
	if j != nil {
		if rerr := env.SetResult(j.OpResult()); rerr != nil {
			env.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", rerr))
		}
	}
	var refused *usermerge.RefusedError
	if errors.As(err, &refused) {
		_ = env.SetResult(refused.Plan.Summary())
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", UserMergeOpID, action, err)
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("%s: journal %s %s (%d ops)", action, j.ID, j.Status, len(j.Ops)))
	return nil
}

// previewUserMerge writes nothing: it reports the plan (apply) or the journal
// (resume, undo).
//
// Under core identity an apply preview run from here FAILS CLOSED: the books
// half is planned from books' own rows, but the core half is planned by
// auth.Admin.PlanUserMerge, which acts as the signed-in admin and needs the
// request's principal in the context. A queued operation has none (the
// principal rides only in the single-use grant the merge routes mint, and a
// preview run mints no grant), so core denies the plan and this preview reports
// that denial instead of a plan. That is intended, not a gap to paper over with
// a synthesized principal: the preview an admin confirms is the one the Users
// page's own preview route builds with the request's principal.
func previewUserMerge(ctx context.Context, params userMergeParams, action usermerge.Action, actor string, env userMergeEnv) error {
	if action == usermerge.ActionApply {
		plan, err := env.Store.BuildPlan(ctx, usermerge.PlanRequest{ActorID: actor, SurvivorID: params.SurvivorID, AbsorbedID: params.AbsorbedID})
		if err != nil {
			return err
		}
		env.Progress(1, 1, fmt.Sprintf("Preview: %d writes, %d conflicts, %d refusals (nothing written)", plan.Ops, plan.ConflictTotal, len(plan.Refusals)))
		return env.SetResult(plan.Summary())
	}
	j, err := env.Store.LoadJournal(params.JournalID)
	if err != nil {
		return err
	}
	env.Progress(1, 1, fmt.Sprintf("Preview: journal %s is %s (nothing written)", j.ID, j.Status))
	return env.SetResult(j.OpResult())
}
