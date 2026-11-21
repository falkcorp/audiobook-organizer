// file: internal/books/cutover/rename_convert.go
// version: 1.0.0
// guid: 97a7e8c6-6bb0-4d2f-b96e-0d0a74a0a2fc
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
)

// The converters a rename-table row names (05-config §1.12.3's Convert
// column). A converter turns HEAD's effective stored value (JSON, as the
// blob holds it) into the canonical key's JSON. It never sees a secret: the
// rows that carry one are class s and are not converted.

// Converter names. "" is the identity.
const (
	ConvertNeverStored               = "never-stored"          // the value is never stored (database_path)
	ConvertAuthMode                  = "auth-mode"             // enable_auth bool -> core.auth.mode
	ConvertDaysToDuration            = "days-to-duration"      // n days -> a duration
	ConvertMiBToBytes                = "mib-to-bytes"          // n MiB -> bytes
	ConvertLogRetention              = "log-retention"         // a negative value keeps forever (0)
	ConvertOIDCFold                  = "oidc-fold"             // the oauth_* rows folded into core.auth.oidc.providers
	ConvertUpdaterMode               = "updater-mode"          // auto_update.enabled -> core.updater.mode
	ConvertUpdaterChannel            = "updater-channel"       // develop -> prerelease
	ConvertUpdaterCheck              = "updater-check"         // minutes -> {enabled, every}
	ConvertScheduleStandard          = "schedule-standard"     // {enabled, interval(min), on_startup} -> tasks.ScheduleConfig
	ConvertScheduleInterval          = "schedule-interval"     // an interval-only entry: enabled iff the interval is above 0
	ConvertScheduleFullScan          = "schedule-full-scan"    // library_scan_full: period_hours -> every
	ConvertScheduleZeroDaily         = "schedule-zero-daily"   // a standard entry whose interval 0 HEAD runs as 24h (ai_dedup_batch)
	ConvertScheduleLibraryScan       = "schedule-library-scan" // library_scan: always enabled; its enabled is modules.books.scan.periodic_enabled
	ConvertCSVList                   = "csv-list"              // "a, b" -> ["a", "b"]
	errNotStagedMessage              = "cutover: the value stages nothing"
	errConvertInputMessage           = "cutover: converter input"
	minutesPerScheduleUnit           = time.Minute
	hoursPerSchedulePeriod           = time.Hour
	hoursPerDay                      = 24 * time.Hour
	bytesPerMiB                int64 = 1 << 20
)

// ErrNotStaged is returned by a converter whose input means "use the
// default" or "never stored": the row stages nothing.
var ErrNotStaged = errors.New(errNotStagedMessage)

// ErrNullUnset is returned by every converter for a stored JSON null. HEAD
// decoded a null into the value it already held (the default), so a null means
// "unset": nothing is staged and the key keeps its default. The planner lists
// each such key in the cutover report. Without this rule a null would decode
// into a bool or a number as its zero value, which for a boolean is "false".
var ErrNullUnset = errors.New("cutover: a stored null means unset")

// ErrConvertInput wraps a converter's refusal of its input's shape. The
// error never carries the value.
var ErrConvertInput = errors.New(errConvertInputMessage)

// knownConvert reports whether name is a converter this package defines.
func knownConvert(name string) bool { return slices.Contains(converterNames, name) }

// converterNames is every converter name Convert implements ("" is the identity),
// so a test can run each of them against the same input.
var converterNames = []string{"", ConvertNeverStored, ConvertAuthMode, ConvertDaysToDuration, ConvertMiBToBytes,
	ConvertLogRetention, ConvertOIDCFold, ConvertUpdaterMode, ConvertUpdaterChannel, ConvertUpdaterCheck,
	ConvertScheduleStandard, ConvertScheduleInterval, ConvertScheduleFullScan, ConvertScheduleZeroDaily, ConvertScheduleLibraryScan, ConvertCSVList}

// isScheduleConvert reports whether name converts a scheduled entry.
func isScheduleConvert(name string) bool {
	switch name {
	case ConvertScheduleStandard, ConvertScheduleInterval, ConvertScheduleFullScan, ConvertScheduleZeroDaily, ConvertScheduleLibraryScan:
		return true
	}
	return false
}

// Convert applies t's converter to in, the value of t's HEAD row. dropped
// lists the input fields the canonical value has no place for (named, never
// valued), which M2 logs. A scheduled entry's value also takes t's window
// placement (placeSchedule): the converter alone writes only what HEAD's entry
// holds, and config stores a schedule key's value whole, so a value without the
// placement would take the task out of its window.
func Convert(t Target, in json.RawMessage) (out json.RawMessage, dropped []string, err error) {
	out, dropped, err = convert(t.Convert, in)
	if err != nil || !isScheduleConvert(t.Convert) {
		return out, dropped, err
	}
	return placeSchedule(t, out, dropped)
}

// placeSchedule completes a converted schedule value with t's placement: the
// window HEAD ran the task in (its maintenanceOrder, frozen as
// headMaintenanceOrder) and its position there. An entry HEAD held enabled
// with no timer then runs window-only, as HEAD ran it; one with no timer and
// no window HEAD never ran (its scheduler logged "can NEVER run"), so it is
// carried disabled and its enabled is reported dropped: config refuses an
// enabled schedule with no timing.
func placeSchedule(t Target, out json.RawMessage, dropped []string) (json.RawMessage, []string, error) {
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, nil, fmt.Errorf("%w: %s: the converted schedule is not an object", ErrConvertInput, t.Convert)
	}
	if t.Window != "" {
		v["window"] = t.Window
		if t.Order != 0 {
			v["order"] = t.Order
		}
	}
	if on, _ := v["enabled"].(bool); on && v["every"] == nil && v["window"] == nil {
		v["enabled"] = false
		dropped = append(dropped, "enabled")
	}
	return mustJSON(v), dropped, nil
}

// convert applies converter name to in (Convert without the target's
// placement; the converters' own tests call it by name).
func convert(name string, in json.RawMessage) (out json.RawMessage, dropped []string, err error) {
	bad := func(what string) error { return fmt.Errorf("%w: %s: %s", ErrConvertInput, name, what) }
	// A converter that stages nothing for any input says so for a null too.
	if name == ConvertNeverStored || name == ConvertOIDCFold {
		return nil, nil, ErrNotStaged
	}
	// A stored null is "unset" for every other converter, before any of them
	// decodes it: encoding/json leaves a bool or a number at its zero value on a
	// null without an error, and a zero value is a setting (auth-mode would read
	// it as "disabled").
	if bytes.Equal(bytes.TrimSpace(in), []byte("null")) {
		return nil, nil, ErrNullUnset
	}
	switch name {
	case "":
		return in, nil, nil
	case ConvertAuthMode:
		var b bool
		if json.Unmarshal(in, &b) != nil {
			return nil, nil, bad("want a boolean")
		}
		if b {
			return json.RawMessage(`"required"`), nil, nil
		}
		return json.RawMessage(`"disabled"`), nil, nil
	case ConvertDaysToDuration:
		n, err := number(in)
		if err != nil {
			return nil, nil, bad("want a number of days")
		}
		return marshalDuration(time.Duration(n * float64(hoursPerDay)))
	case ConvertMiBToBytes:
		n, err := number(in)
		if err != nil {
			return nil, nil, bad("want a number of MiB")
		}
		return json.RawMessage(fmt.Sprintf("%d", int64(math.Round(n))*bytesPerMiB)), nil, nil
	case ConvertLogRetention:
		n, err := number(in)
		if err != nil {
			return nil, nil, bad("want a number of days")
		}
		if n < 0 {
			n = 0
		}
		return json.RawMessage(fmt.Sprintf("%d", int64(n))), nil, nil
	case ConvertUpdaterMode:
		var b bool
		if err := json.Unmarshal(in, &b); err != nil {
			return nil, nil, bad("want a boolean")
		}
		if b {
			return json.RawMessage(`"check"`), nil, nil
		}
		return json.RawMessage(`"off"`), nil, nil
	case ConvertUpdaterChannel:
		var s string
		if err := json.Unmarshal(in, &s); err != nil {
			return nil, nil, bad("want a string")
		}
		if s == "develop" {
			s = "prerelease"
		}
		return mustJSON(s), nil, nil
	case ConvertUpdaterCheck:
		n, err := number(in)
		if err != nil {
			return nil, nil, bad("want a number of minutes")
		}
		if n <= 0 {
			return nil, nil, ErrNotStaged
		}
		return mustJSON(map[string]any{"enabled": true, "every": config.Duration(time.Duration(n * float64(minutesPerScheduleUnit)))}), nil, nil
	case ConvertScheduleStandard, ConvertScheduleInterval, ConvertScheduleFullScan, ConvertScheduleZeroDaily, ConvertScheduleLibraryScan:
		return convertSchedule(name, in, bad)
	case ConvertCSVList:
		var s string
		if err := json.Unmarshal(in, &s); err != nil {
			return nil, nil, bad("want a string")
		}
		var list []string
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				list = append(list, part)
			}
		}
		if len(list) == 0 {
			return nil, nil, ErrNotStaged
		}
		return mustJSON(list), nil, nil
	}
	return nil, nil, fmt.Errorf("%w: unknown converter %q", ErrConvertInput, name)
}

// convertSchedule turns one scheduled entry into a tasks.ScheduleConfig, by
// HEAD's own reading of the entry (config.go:991-1046): a standard entry's
// Enabled and Interval (minutes) both count; an interval-only entry runs iff
// its interval is above 0 and ignores Enabled; library_scan_full runs its
// sweep every period_hours. OnStartup has no ScheduleConfig field: it is
// reported as dropped when set.
func convertSchedule(name string, in json.RawMessage, bad func(string) error) (json.RawMessage, []string, error) {
	var e struct {
		Enabled     bool `json:"enabled"`
		Interval    int  `json:"interval"`
		OnStartup   bool `json:"on_startup"`
		PeriodHours int  `json:"period_hours"`
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	if err := dec.Decode(&e); err != nil {
		return nil, nil, bad("want a scheduled entry object")
	}
	out := map[string]any{}
	var dropped []string
	switch name {
	case ConvertScheduleInterval:
		out["enabled"] = e.Interval > 0
		if e.Interval > 0 {
			out["every"] = config.Duration(time.Duration(e.Interval) * minutesPerScheduleUnit)
		}
	case ConvertScheduleFullScan:
		out["enabled"] = e.Enabled
		if e.PeriodHours > 0 {
			out["every"] = config.Duration(time.Duration(e.PeriodHours) * hoursPerSchedulePeriod)
		}
	case ConvertScheduleLibraryScan:
		// The key stays enabled: HEAD's entry enabled gates only the timer and,
		// with scan_on_startup, the window run, and it moves to its own key
		// (scheduled.library_scan.enabled -> modules.books.scan.periodic_enabled),
		// which the kind's runtime gates read. HEAD's interval 0 is no timer.
		out["enabled"] = true
		if e.Interval > 0 {
			out["every"] = config.Duration(time.Duration(e.Interval) * minutesPerScheduleUnit)
		}
	case ConvertScheduleZeroDaily:
		// HEAD's GetInterval reads an interval at or below 0 as 24h
		// (scheduler/tasks.go, ai_dedup_batch), so the entry always has a timer.
		out["enabled"] = e.Enabled
		every := time.Duration(e.Interval) * minutesPerScheduleUnit
		if e.Interval <= 0 {
			every = hoursPerDay
		}
		out["every"] = config.Duration(every)
	default:
		out["enabled"] = e.Enabled
		if e.Interval > 0 {
			out["every"] = config.Duration(time.Duration(e.Interval) * minutesPerScheduleUnit)
		}
	}
	if e.OnStartup && (name == ConvertScheduleStandard || name == ConvertScheduleZeroDaily || name == ConvertScheduleLibraryScan) {
		dropped = append(dropped, "on_startup")
	}
	return mustJSON(out), dropped, nil
}

func number(in json.RawMessage) (float64, error) {
	var n float64
	if err := json.Unmarshal(in, &n); err != nil {
		return 0, err
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, errors.New("not finite")
	}
	return n, nil
}

func marshalDuration(d time.Duration) (json.RawMessage, []string, error) {
	b, err := json.Marshal(config.Duration(d))
	if err != nil {
		return nil, nil, err
	}
	return b, nil, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("cutover: marshal of a converter value: " + err.Error())
	}
	return b
}
