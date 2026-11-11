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
	ConvertNeverStored            = "never-stored"       // the value is never stored (database_path)
	ConvertAuthMode               = "auth-mode"          // enable_auth bool -> core.auth.mode
	ConvertDaysToDuration         = "days-to-duration"   // n days -> a duration
	ConvertMiBToBytes             = "mib-to-bytes"       // n MiB -> bytes
	ConvertLogRetention           = "log-retention"      // a negative value keeps forever (0)
	ConvertOIDCFold               = "oidc-fold"          // the oauth_* rows folded into core.auth.oidc.providers
	ConvertUpdaterMode            = "updater-mode"       // auto_update.enabled -> core.updater.mode
	ConvertUpdaterChannel         = "updater-channel"    // develop -> prerelease
	ConvertUpdaterCheck           = "updater-check"      // minutes -> {enabled, every}
	ConvertScheduleStandard       = "schedule-standard"  // {enabled, interval(min), on_startup} -> tasks.ScheduleConfig
	ConvertScheduleInterval       = "schedule-interval"  // an interval-only entry: enabled iff the interval is above 0
	ConvertScheduleFullScan       = "schedule-full-scan" // library_scan_full: period_hours -> every
	ConvertCSVList                = "csv-list"           // "a, b" -> ["a", "b"]
	errNotStagedMessage           = "cutover: the value stages nothing"
	errConvertInputMessage        = "cutover: converter input"
	minutesPerScheduleUnit        = time.Minute
	hoursPerSchedulePeriod        = time.Hour
	hoursPerDay                   = 24 * time.Hour
	bytesPerMiB             int64 = 1 << 20
)

// ErrNotStaged is returned by a converter whose input means "use the
// default" or "never stored": the row stages nothing.
var ErrNotStaged = errors.New(errNotStagedMessage)

// ErrConvertInput wraps a converter's refusal of its input's shape. The
// error never carries the value.
var ErrConvertInput = errors.New(errConvertInputMessage)

// knownConvert reports whether name is a converter this package defines.
func knownConvert(name string) bool {
	switch name {
	case "", ConvertNeverStored, ConvertAuthMode, ConvertDaysToDuration, ConvertMiBToBytes, ConvertLogRetention,
		ConvertOIDCFold, ConvertUpdaterMode, ConvertUpdaterChannel, ConvertUpdaterCheck, ConvertScheduleStandard,
		ConvertScheduleInterval, ConvertScheduleFullScan, ConvertCSVList:
		return true
	}
	return false
}

// Convert applies converter name to in. dropped lists the input fields the
// canonical value has no place for (named, never valued), which M2 logs.
func Convert(name string, in json.RawMessage) (out json.RawMessage, dropped []string, err error) {
	bad := func(what string) error { return fmt.Errorf("%w: %s: %s", ErrConvertInput, name, what) }
	switch name {
	case "":
		return in, nil, nil
	case ConvertNeverStored, ConvertOIDCFold:
		return nil, nil, ErrNotStaged
	case ConvertAuthMode:
		var b bool
		if err := json.Unmarshal(in, &b); err != nil {
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
	case ConvertScheduleStandard, ConvertScheduleInterval, ConvertScheduleFullScan:
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
	default:
		out["enabled"] = e.Enabled
		if e.Interval > 0 {
			out["every"] = config.Duration(time.Duration(e.Interval) * minutesPerScheduleUnit)
		}
	}
	if e.OnStartup && name == ConvertScheduleStandard {
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
