// file: core/kernel/core_sections_validate.go
// version: 1.1.0
// guid: adedcea5-5d4b-4dd0-b8c2-12da76e1f988
// last-edited: 2026-11-13

package kernel

import (
	"errors"
	"reflect"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// Section validation of the schedule keys (F2-04, 05-tasks 5T.3.8). Every
// section that holds a schedules group implements config.Validator by
// checking each tasks.ScheduleConfig key of that group with
// tasks.ScheduleConfig.Validate, so a bad value (two timings at once, a cron
// that does not parse, a daily_at that is not HH:MM, a negative every) is
// refused when config loads or applies it, not at the scheduler's first poll.
// The rule that every >= core.tasks.schedule_poll reads another key, so it
// stays the scheduler's check at poll time. These methods are in their own
// file, beside core_sections.go's declarations, so that file stays the
// key table.

// validateSchedules checks every tasks.ScheduleConfig field of group (a
// struct such as aiSchedules) and returns the failures as *config.FieldError
// on each key's path, base being the group's own path (core.ai.schedules). It
// walks the fields, so a schedule key added to a group is checked without
// editing the section's Validate; TestCoreSections_ScheduleValidation pins
// that every group is reached.
func validateSchedules(base config.Path, group any) error {
	v := reflect.ValueOf(group)
	var errs []error
	for _, f := range reflect.VisibleFields(v.Type()) {
		if f.Type != reflect.TypeFor[tasks.ScheduleConfig]() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		c, _ := v.FieldByIndex(f.Index).Interface().(tasks.ScheduleConfig)
		if err := c.Validate(); err != nil {
			errs = append(errs, &config.FieldError{Path: base + "." + config.Path(name), Err: err})
		}
	}
	return errors.Join(errs...)
}

// Validate implements config.Validator (core.ai.schedules).
func (s aiSettings) Validate() error { return validateSchedules("core.ai.schedules", s.Schedules) }

// Validate implements config.Validator (core.auth.schedules).
func (s authSettings) Validate() error {
	return validateSchedules("core.auth.schedules", s.Schedules)
}

// Validate implements config.Validator (core.backup.schedules).
func (s backupSettings) Validate() error {
	return validateSchedules("core.backup.schedules", s.Schedules)
}

// Validate implements config.Validator (core.diagnostics.schedules).
func (s diagnosticsSettings) Validate() error {
	return validateSchedules("core.diagnostics.schedules", s.Schedules)
}

// Validate implements config.Validator (core.library.schedules).
func (s librarySettings) Validate() error {
	return validateSchedules("core.library.schedules", s.Schedules)
}

// core.log.schedules is a section of its own whose type, logjournal.Schedules,
// implements config.Validator itself (F5-15), so it has no method here.

// Validate implements config.Validator (core.integrations.schedules).
func (s integrationsSettings) Validate() error {
	return validateSchedules("core.integrations.schedules", s.Schedules)
}

// Validate implements config.Validator (core.audit.schedules).
func (s auditSettings) Validate() error {
	return validateSchedules("core.audit.schedules", s.Schedules)
}

// Validate implements config.Validator (core.storage.schedules).
func (s storageSettings) Validate() error {
	return validateSchedules("core.storage.schedules", s.Schedules)
}

// Validate implements config.Validator (core.tasks.schedules).
func (s tasksSettings) Validate() error {
	return validateSchedules("core.tasks.schedules", s.Schedules)
}

// Validate implements config.Validator (core.updater.schedules).
func (s updaterSettings) Validate() error {
	return validateSchedules("core.updater.schedules", s.Schedules)
}
