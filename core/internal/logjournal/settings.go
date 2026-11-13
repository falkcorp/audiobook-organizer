// file: core/internal/logjournal/settings.go
// version: 1.1.0
// guid: 5a9e3d71-c204-4f8b-b6a0-8e1d7c3f9b52
// last-edited: 2026-11-13

package logjournal

import (
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// Settings is core.log.journal (05-logging §8). The kernel registers it next
// to core.log, whose type (log.Settings) names neither of the journal's two
// sections.
//
// Struct tags follow 05-config §1.3.1. Enabled and QueueSize are restart keys:
// the tap is started once with its queue, and a hot flip of either would mean
// tearing a tap down under the logger.
type Settings struct {
	// Enabled starts the journal tap. Off, the journal is neither written nor
	// pruned, and what is already in it stays readable.
	Enabled bool `json:"enabled" reload:"restart"`
	// MinLevel is the least severe record the journal keeps: debug, info, warn
	// or error. It is independent of the primary output's level.
	MinLevel string `json:"min_level" schema:"enum=debug|info|warn|error"`
	// RetentionDays is how long a record is kept; 0 keeps everything.
	RetentionDays int `json:"retention_days" schema:"minimum=0"`
	// QueueSize is the tap's queue; a full queue drops the newest records and
	// counts them under mediacore_log_dropped_total{sink="journal"}.
	QueueSize int `json:"queue_size" reload:"restart" schema:"minimum=64"`
}

// Schedules is core.log.schedules (05-logging §8, R2-7): the journal's one
// scheduled task, whose key the prune task reads. The kernel's default is
// every 24 hours.
type Schedules struct {
	JournalPrune tasks.ScheduleConfig `json:"journal_prune"`
}

// SchedulesPath is core.log.schedules, the section's path.
const SchedulesPath config.Path = "core.log.schedules"

// Validate implements config.Validator: a bad journal_prune value (two timings
// at once, a cron that does not parse, a negative every) is refused when
// config loads or applies it, not at the scheduler's first poll (05-tasks
// 5T.3.8). The rule that every >= core.tasks.schedule_poll reads another key,
// so it stays the scheduler's check.
func (s Schedules) Validate() error {
	if err := s.JournalPrune.Validate(); err != nil {
		return &config.FieldError{Path: SchedulesPath + ".journal_prune", Err: err}
	}
	return nil
}
