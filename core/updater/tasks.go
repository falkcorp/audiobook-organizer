// file: core/updater/tasks.go
// version: 1.0.0
// guid: 8a3c6e21-d594-4f07-b2e8-1f90a7c35d46
// last-edited: 2026-11-18

package updater

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The kinds of 05-updater 5U.4.
const (
	KindCheck    tasks.Kind = "core.updater.check"
	KindApply    tasks.Kind = "core.updater.apply"
	KindRollback tasks.Kind = "core.updater.rollback"
)

// ScheduleCheckKey is the check task's schedule key, in the updater's own
// namespace (R2-7; 05-updater 5U.8).
const ScheduleCheckKey = "core.updater.schedules.check"

// taskKey is the concurrency key apply and rollback share: one swap of the
// binary at a time (5U.7).
const taskKey tasks.Key = "core.updater"

// seenKey is the one durable key of the updater, in c/updater/ (5U.4): the
// last version a notification went out for.
const seenKey = "seen"

// FoundTopicName is the catalog name of the update-found topic.
const FoundTopicName = "core.update.found.v1"

// FoundPayload is core.update.found.v1's payload: the same fields the
// notification carries, for UI refresh.
type FoundPayload struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Channel string `json:"channel"`
	URL     string `json:"url,omitempty"`
}

// FoundTopic is the typed topic. A function, not a package variable (R5).
func FoundTopic() events.Topic[FoundPayload] { return events.NewTopic[FoundPayload](FoundTopicName) }

// FoundTopicSpec is the updater's catalog entry: a bus topic observable by
// core:system.manage that may be emitted as a notification (05-notify's
// outbox-backed Publish stages it; the bus publish alone notifies nobody,
// COR2-3). The kernel declares it with core's other topics, in F8-04
// (kernel.coreTopics), when it wires the updater.
func FoundTopicSpec() events.TopicSpec {
	return FoundTopic().Spec(events.TopicSpec{
		Permission: rbac.Need("core:system.manage"),
		Notify:     &events.NotifyMeta{Title: "Update available", DefaultSeverity: "info"},
	})
}

// Empty is an empty parameter or result.
type Empty struct{}

// ApplyParams are core.updater.apply's parameters: the route's body. An empty
// version applies the release the last check offered.
type ApplyParams struct {
	Version string `json:"version,omitempty"`
}

// StatusView is the JSON form of Status: what the check task reports and the
// status route serves.
type StatusView struct {
	Mode            string     `json:"mode"`
	Channel         string     `json:"channel"`
	Current         string     `json:"current"`
	Latest          string     `json:"latest,omitempty"`
	Available       bool       `json:"available"`
	LastChecked     *time.Time `json:"last_checked,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	PreviousKept    string     `json:"previous_kept,omitempty"`
	DisabledReason  string     `json:"disabled_reason,omitempty"`
	LocalBuild      bool       `json:"local_build,omitempty"`
	LocalBuildNote  string     `json:"local_build_note,omitempty"`
	AutoApplyPaused bool       `json:"auto_apply_paused,omitempty"`
}

// View is the JSON form of s.
func (s Status) View() StatusView {
	v := StatusView{
		Mode: s.Mode.String(), Channel: string(s.Channel), Current: s.Current, Latest: s.Latest,
		Available: s.Available, LastError: s.LastError, PreviousKept: s.PreviousKept,
		DisabledReason: s.DisabledReason, LocalBuild: s.LocalBuild, AutoApplyPaused: s.AutoApplyPaused,
	}
	if !s.LastChecked.IsZero() {
		t := s.LastChecked
		v.LastChecked = &t
	}
	if s.LocalBuild {
		v.LocalBuildNote = LocalBuildNote
	}
	return v
}

// Resource names what each task writes: the updater's own state (the seen
// key), and the executable.
func resourceUpdater() entity.Ref { return tasks.Res("core", "updater") }
func resourceBinary() entity.Ref  { return tasks.Res("core", "binary") }

// The three core.updater.* definitions, one constructor each. The kernel
// passes them to Registrar.Tasks at P6 once the app sets app.Options.Updater
// (F8-04); until then nothing registers them. A nil u yields a definition
// whose Run is nil, which tasks.Problems reports ("Task has no Run"), so
// registration fails closed instead of panicking when a task starts.

// CheckTask is core.updater.check: it asks the release source for the newest
// release, records the status and, the first time a version appears, stages
// the notification. It runs on the schedule core.updater.schedules.check
// while mode is check, and the check route starts it in live mode. It never
// applies anything.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 from F8-04; this package registers nothing
func CheckTask(u *Updater) tasks.Definition {
	var run func(*tasks.RC, Empty) (StatusView, error)
	sched := tasks.ScheduleFromConfig(ScheduleCheckKey)
	if u != nil {
		run = u.taskCheck
		sched = sched.When(func() bool { return u.settings().Mode >= ModeCheck })
	}
	return tasks.Task(KindCheck, tasks.TaskSpec[Empty, StatusView]{
		Common: tasks.Common{
			Title: "Check for updates", Help: "Asks the release source for the newest release and records whether it is newer than the running build. It never installs anything.",
			Effects: tasks.Writes(resourceUpdater()), Perms: tasks.Permissions{Start: rbac.Need("core:system.manage")}, Resume: tasks.ResumeDrop,
			Priority: tasks.PrioritySystem, Timeout: 5 * time.Minute,
			Schedule: sched.Live(), Dedupe: tasks.DedupePolicy{AnyParams: true},
		},
		Run: run,
	})
}

// ApplyTask is core.updater.apply: HEAD's raw-asset download and rename swap
// of the running binary, then the restart. It has no Schedule: it runs only
// when an admin starts it (5U.4).
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 from F8-04; this package registers nothing
func ApplyTask(u *Updater) tasks.Definition {
	var run func(*tasks.RC, ApplyParams) (Empty, error)
	if u != nil {
		run = u.taskApply
	}
	return tasks.Task(KindApply, tasks.TaskSpec[ApplyParams, Empty]{
		Common: tasks.Common{
			Title: "Install an update", Help: "Downloads a release, checks it runs, replaces the running binary keeping the old one, and restarts. Only an admin starts it.",
			Effects: tasks.Writes(resourceBinary()), Perms: tasks.Permissions{Start: rbac.Need("core:admin")}, Resume: tasks.ResumeDrop,
			ConcurrencyKey: taskKey, Cancel: tasks.Cooperative(30 * time.Second), Priority: tasks.PrioritySystem, Timeout: 30 * time.Minute,
		},
		Run: run,
	})
}

// RollbackTask is core.updater.rollback: it checks that the kept previous
// binary can serve the store, swaps it back and restarts.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks at P6 from F8-04; this package registers nothing
func RollbackTask(u *Updater) tasks.Definition {
	var run func(*tasks.RC, Empty) (Empty, error)
	if u != nil {
		run = u.taskRollback
	}
	return tasks.Task(KindRollback, tasks.TaskSpec[Empty, Empty]{
		Common: tasks.Common{
			Title: "Roll back an update", Help: "Puts the previous binary back after checking that it can still read the data, then restarts.",
			Effects: tasks.Writes(resourceBinary()), Perms: tasks.Permissions{Start: rbac.Need("core:admin")}, Resume: tasks.ResumeDrop,
			ConcurrencyKey: taskKey, Cancel: tasks.Cooperative(30 * time.Second), Priority: tasks.PrioritySystem, Timeout: 10 * time.Minute,
		},
		Run: run,
	})
}

func (u *Updater) taskCheck(rc *tasks.RC, _ Empty) (StatusView, error) {
	return u.runCheck(rc.Context(), rc.Writer())
}

func (u *Updater) taskApply(rc *tasks.RC, p ApplyParams) (Empty, error) {
	return Empty{}, u.Apply(rc, p.Version)
}

func (u *Updater) taskRollback(rc *tasks.RC, _ Empty) (Empty, error) {
	return Empty{}, u.Rollback(rc)
}

// runCheck is the check task's body over w, the run's gated writer.
func (u *Updater) runCheck(ctx context.Context, w tasks.Writer) (StatusView, error) {
	st, rel, err := u.check(ctx, CheckOptions{})
	if err != nil {
		return st.View(), err
	}
	if st.Available && st.LastError == "" {
		if err := u.announce(ctx, w, st, rel); err != nil {
			return st.View(), fmt.Errorf("update found, but recording and announcing it failed: %w", err)
		}
	}
	return st.View(), nil
}

// seenRecord is the value of c/updater/seen.
type seenRecord struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// announce notifies once per version across restarts: when st.Latest is not
// the version c/updater/seen holds, it writes the key and stages the
// notification in the SAME batch (so each version notifies exactly once), then
// publishes the topic on the bus for UI refresh. A store the updater was not
// given records and announces nothing.
func (u *Updater) announce(ctx context.Context, w tasks.Writer, st Status, rel Release) error {
	if u.d.Store.Prefix() == "" {
		return nil
	}
	seen, err := u.readSeen(ctx)
	if err != nil {
		return err
	}
	if seen.Version == st.Latest {
		return nil
	}
	if w == nil {
		return errors.New("the run has no writer")
	}
	subject := entity.Ref{Module: "core", Kind: "updater", ID: seenKey}
	write := tasks.Write{Name: "core.updater.seen", Resource: resourceUpdater(), Subject: subject}
	err = w.Do(ctx, write, func(ctx context.Context) ([]tasks.Change, error) {
		b := u.d.Store.NewBatch()
		defer func() { _ = b.Close() }()
		val, err := json.Marshal(seenRecord{Version: st.Latest, At: u.now().UTC()})
		if err != nil {
			return nil, err
		}
		if err := b.Set([]byte(seenKey), val); err != nil {
			return nil, err
		}
		if u.d.Notify != nil {
			if err := u.d.Notify.Publish(ctx, b, u.foundNotification(st, rel)); err != nil {
				return nil, fmt.Errorf("stage the notification: %w", err)
			}
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			return nil, err
		}
		return []tasks.Change{{Subject: subject, Field: "version", Old: seen.Version, New: st.Latest}}, nil
	})
	if errors.Is(err, tasks.ErrPreview) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.d.Events != nil {
		events.Publish(u.d.Events, ctx, FoundTopic(), st.Latest, FoundPayload{
			Current: st.Current, Latest: st.Latest, Channel: string(st.Channel), URL: safeURL(rel.URL),
		})
	}
	return nil
}

func (u *Updater) readSeen(ctx context.Context) (seenRecord, error) {
	var rec seenRecord
	raw, err := u.d.Store.Get(ctx, []byte(seenKey))
	if errors.Is(err, storage.ErrNotFound) {
		return rec, nil
	}
	if err != nil {
		return rec, fmt.Errorf("read the last announced version: %w", err)
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		// An unreadable record is as good as none: the version is announced
		// again and the record rewritten.
		u.logf(slog.LevelWarn, "the last announced version does not read; announcing again")
		return seenRecord{}, nil
	}
	return rec, nil
}

// foundNotification is the notification for a newly seen version.
func (u *Updater) foundNotification(st Status, rel Release) notify.Notification {
	n := notify.Notification{
		Topic: FoundTopicName,
		Title: "Update available",
		Body:  fmt.Sprintf("%s %s is available; this build is %s.", u.opts.BinaryName, st.Latest, st.Current),
		Fields: map[string]string{
			"current": st.Current, "latest": st.Latest, "channel": string(st.Channel),
		},
	}
	if link := safeURL(rel.URL); link != "" {
		n.Links = []notify.Link{{Label: "Release notes", URL: link}}
	}
	return n
}

// safeURL returns raw when it is an absolute http(s) URL, else "".
func safeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.String()
}
