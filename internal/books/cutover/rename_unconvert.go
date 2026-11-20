// file: internal/books/cutover/rename_unconvert.go
// version: 1.0.1
// guid: d042fa77-8702-4e8f-8fee-47d908a4ca77
// last-edited: 2026-11-19

package cutover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
)

// The inverse of rename_convert.go: a canonical key's JSON turned back into
// HEAD's effective value, for the layer that writes a canonical key through to
// the blob HEAD's release still reads (F3-07 e2). Everything here is a pure
// function of its argument.
//
// An inverse is the other half of Convert only where Convert loses nothing.
// Where it does, the inverse is deliberate about the gap, and each case is
// pinned by TestUnconvert_LossyCases:
//   - A canonical value HEAD cannot hold (a fractional day, a cron schedule, a
//     mode HEAD has no name for) is refused with ErrNotRepresentable, never
//     rounded: a retention or schedule that silently differs from what the
//     operator wrote is worse than a refused write.
//   - A HEAD field the canonical value has no place for (on_startup, a full
//     scan's due-check interval, an interval-only entry's enabled) is not
//     written. The result of an entry converter is the fields the canonical
//     value determines, to be overlaid on HEAD's existing entry; the caller
//     keeps the rest.

// The messages of the inverse-only refusals; none carries a value.
const (
	errNotApplicableMessage    = "cutover: the converter has no inverse"
	errNotRepresentableMessage = "cutover: the canonical value cannot be held at HEAD"
	errUnknownConverterMessage = "cutover: no such converter"
)

// ErrNotApplicable is returned by Unconvert for a converter with nothing to
// give back: never-stored (the value is never stored) and oidc-fold (see
// UnfoldOIDC), the two Convert answers with ErrNotStaged. An unknown name
// wraps it too.
var ErrNotApplicable = errors.New(errNotApplicableMessage)

// ErrNotRepresentable wraps a canonical value that is well formed but that
// HEAD's field cannot hold. The error never carries the value.
var ErrNotRepresentable = errors.New(errNotRepresentableMessage)

// errUnknownConverter marks a name this package defines no converter for, so a
// test can tell "no inverse written" from "stages nothing".
var errUnknownConverter = errors.New(errUnknownConverterMessage)

// HEAD's keep-forever log retention (core/log LogRetentionKeepForever): HEAD
// reads a stored 0 as the 90-day default, so canonical "keep forever" (0) is -1
// there.
const headLogRetentionKeepForever = -1

// Unconvert applies the inverse of converter name to in, a canonical key's
// JSON, and returns HEAD's value. The converters that stage nothing return
// ErrNotApplicable.
func Unconvert(name string, in json.RawMessage) (json.RawMessage, error) {
	bad := func(what string) error { return fmt.Errorf("%w: %s: %s", ErrConvertInput, name, what) }
	unrep := func(what string) error { return fmt.Errorf("%w: %s: %s", ErrNotRepresentable, name, what) }
	switch name {
	case "":
		return in, nil
	case ConvertNeverStored, ConvertOIDCFold:
		return nil, fmt.Errorf("%w: %s", ErrNotApplicable, name)
	case ConvertAuthMode:
		var s string
		if err := json.Unmarshal(in, &s); err != nil {
			return nil, bad("want a string")
		}
		switch s {
		case "required":
			return json.RawMessage(`true`), nil
		case "disabled":
			return json.RawMessage(`false`), nil
		}
		return nil, unrep("HEAD has only on and off")
	case ConvertDaysToDuration:
		var d config.Duration
		if err := json.Unmarshal(in, &d); err != nil {
			return nil, bad("want a duration string")
		}
		days, ok := wholeUnits(d.Std(), hoursPerDay)
		if !ok {
			return nil, unrep("HEAD counts whole days")
		}
		return mustJSON(days), nil
	case ConvertMiBToBytes:
		n, err := number(in)
		if err != nil {
			return nil, bad("want a number of bytes")
		}
		if n != math.Trunc(n) || n < 0 || n > maxExactFloatInt {
			return nil, unrep("HEAD counts whole MiB")
		}
		if int64(n)%bytesPerMiB != 0 {
			return nil, unrep("HEAD counts whole MiB")
		}
		return mustJSON(int64(n) / bytesPerMiB), nil
	case ConvertLogRetention:
		n, err := number(in)
		if err != nil {
			return nil, bad("want a number of days")
		}
		if n != math.Trunc(n) || n < 0 {
			return nil, unrep("HEAD keeps a whole number of days, or -1 for ever")
		}
		if n == 0 {
			return mustJSON(headLogRetentionKeepForever), nil
		}
		return mustJSON(int64(n)), nil
	case ConvertUpdaterMode:
		var s string
		if err := json.Unmarshal(in, &s); err != nil {
			return nil, bad("want a string")
		}
		switch s {
		case "check":
			return json.RawMessage(`true`), nil
		case "off":
			return json.RawMessage(`false`), nil
		}
		return nil, unrep("HEAD has only on and off")
	case ConvertUpdaterChannel:
		var s string
		if err := json.Unmarshal(in, &s); err != nil {
			return nil, bad("want a string")
		}
		if s == "prerelease" {
			s = "develop"
		}
		return mustJSON(s), nil
	case ConvertUpdaterCheck:
		c, err := decodeCanonSchedule(in, bad, unrep)
		if err != nil {
			return nil, err
		}
		if !c.Enabled {
			// HEAD reads any check_minutes at or below 0 as off.
			return mustJSON(0), nil
		}
		if c.Every == 0 {
			return nil, unrep("an enabled check needs an interval")
		}
		min, ok := wholeUnits(c.Every, minutesPerScheduleUnit)
		if !ok {
			return nil, unrep("HEAD counts whole minutes")
		}
		return mustJSON(min), nil
	case ConvertScheduleStandard, ConvertScheduleInterval, ConvertScheduleFullScan:
		return unconvertSchedule(name, in, bad, unrep)
	case ConvertCSVList:
		var list []string
		if err := json.Unmarshal(in, &list); err != nil {
			return nil, bad("want a list of strings")
		}
		return joinCSV(list, unrep)
	}
	return nil, fmt.Errorf("%w: %w: %w: %q", ErrNotApplicable, ErrConvertInput, errUnknownConverter, name)
}

// maxExactFloatInt is the largest integer a float64 holds exactly.
const maxExactFloatInt = 1 << 53

// wholeUnits is d in units when d is a whole, non-negative number of them.
func wholeUnits(d, unit time.Duration) (int64, bool) {
	if d < 0 || d%unit != 0 {
		return 0, false
	}
	return int64(d / unit), true
}

// joinCSV is the inverse of the csv-list split: items joined by a bare comma.
// An item that is empty once trimmed, or holds a comma, would not split back
// into itself, so it is refused.
func joinCSV(list []string, unrep func(string) error) (json.RawMessage, error) {
	items := make([]string, len(list))
	for i, item := range list {
		item = strings.TrimSpace(item)
		if item == "" || strings.Contains(item, ",") {
			return nil, unrep("an item is empty or holds a comma")
		}
		items[i] = item
	}
	return mustJSON(strings.Join(items, ",")), nil
}

// canonSchedule is a tasks.ScheduleConfig, which this package does not import.
type canonSchedule struct {
	Enabled bool            `json:"enabled"`
	Every   config.Duration `json:"every"`
	DailyAt string          `json:"daily_at"`
	Cron    string          `json:"cron"`
	Window  string          `json:"window"`
	Order   int             `json:"order"`
}

// scheduleIntent is what a canonical schedule says that HEAD can hold.
type scheduleIntent struct {
	Enabled bool
	Every   time.Duration // 0: none
}

// decodeCanonSchedule reads a ScheduleConfig and refuses what HEAD's entries
// cannot say: a time of day, a cron line or a maintenance window, and a
// negative interval.
func decodeCanonSchedule(in json.RawMessage, bad, unrep func(string) error) (scheduleIntent, error) {
	var out scheduleIntent
	var c canonSchedule
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return out, bad("want a schedule object")
	}
	if c.DailyAt != "" || c.Cron != "" || c.Window != "" || c.Order != 0 {
		return out, unrep("HEAD schedules by interval only")
	}
	if c.Every.Std() < 0 {
		return out, unrep("a negative interval")
	}
	out.Enabled, out.Every = c.Enabled, c.Every.Std()
	return out, nil
}

// unconvertSchedule is the inverse of convertSchedule: the fields of HEAD's
// scheduled entry the canonical ScheduleConfig determines.
func unconvertSchedule(name string, in json.RawMessage, bad, unrep func(string) error) (json.RawMessage, error) {
	c, err := decodeCanonSchedule(in, bad, unrep)
	if err != nil {
		return nil, err
	}
	switch name {
	case ConvertScheduleInterval:
		// HEAD runs an interval-only task iff its interval is above 0 and never
		// reads Enabled: a disabled value must come back 0 whatever its interval.
		if !c.Enabled {
			return mustJSON(map[string]any{"interval": 0}), nil
		}
		if c.Every == 0 {
			return nil, unrep("an enabled entry needs an interval")
		}
		min, ok := wholeUnits(c.Every, minutesPerScheduleUnit)
		if !ok {
			return nil, unrep("HEAD counts whole minutes")
		}
		return mustJSON(map[string]any{"interval": min}), nil
	case ConvertScheduleFullScan:
		hours, ok := wholeUnits(c.Every, hoursPerSchedulePeriod)
		if !ok {
			return nil, unrep("HEAD counts whole hours")
		}
		// The due-check interval has no canonical field: it is left out.
		return mustJSON(map[string]any{"enabled": c.Enabled, "period_hours": hours}), nil
	}
	min, ok := wholeUnits(c.Every, minutesPerScheduleUnit)
	if !ok {
		return nil, unrep("HEAD counts whole minutes")
	}
	// on_startup has no canonical field: it is left out.
	return mustJSON(map[string]any{"enabled": c.Enabled, "interval": min}), nil
}

// ---- the OIDC fold ----

// The HEAD names the fold reads.
const (
	oauthEnabled     = "oauth_enabled"
	oauthEmails      = "oauth_allowed_emails"
	oauthDefaultRole = "oauth_default_role"
	oauthClientIDFmt = "oauth_%s_client_id"
)

// UnfoldOIDC is the inverse of the OIDC fold: the oauth_* HEAD names that the
// canonical core.auth.oidc.providers list determines, each with its value. The
// client secrets are not among them (the entry holds a reference; the secret
// moves by name).
//
// No entries means sign-in is off (the fold builds none when it is off, since
// an entry would turn SSO on): the map holds oauth_enabled=false alone, and
// leaves HEAD's client ids and allow-list where they are. With entries the map
// holds oauth_enabled=true, both providers' client ids (a provider with no
// entry is ""), and oauth_allowed_emails ("" when no entry has an allow-list).
// oauth_default_role is present only when the entries carry a default_role.
//
// HEAD holds one allow-list and one default role for both providers, so entries
// that disagree on either, an unknown or repeated provider id and an entry
// with no client id are refused with ErrNotRepresentable. An entry's kind,
// issuer, client_secret and any other field are ignored.
func UnfoldOIDC(entries json.RawMessage) (map[string]json.RawMessage, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(entries, &raw); err != nil {
		return nil, fmt.Errorf("%w: oidc-fold: want a list of provider entries", ErrConvertInput)
	}
	if len(raw) == 0 {
		return map[string]json.RawMessage{oauthEnabled: json.RawMessage(`false`)}, nil
	}
	type entry struct {
		ID            string   `json:"id"`
		ClientID      string   `json:"client_id"`
		AllowedEmails []string `json:"allowed_emails"`
		DefaultRole   string   `json:"default_role"`
	}
	unrep := func(what string) error { return fmt.Errorf("%w: oidc-fold: %s", ErrNotRepresentable, what) }
	ids := map[string]string{}
	var emails []string
	var role string
	for i, r := range raw {
		var e entry
		if err := json.Unmarshal(r, &e); err != nil {
			return nil, fmt.Errorf("%w: oidc-fold: want a provider entry object", ErrConvertInput)
		}
		known := false
		for _, p := range oidcProviders {
			known = known || p.id == e.ID
		}
		if !known {
			return nil, unrep("HEAD knows two providers")
		}
		if _, dup := ids[e.ID]; dup {
			return nil, unrep("a provider is listed twice")
		}
		if e.ClientID == "" {
			return nil, unrep("an entry has no client id")
		}
		ids[e.ID] = e.ClientID
		cleaned := make([]string, 0, len(e.AllowedEmails))
		for _, m := range e.AllowedEmails {
			cleaned = append(cleaned, strings.TrimSpace(m))
		}
		if i == 0 {
			emails, role = cleaned, e.DefaultRole
			continue
		}
		if !slices.Equal(emails, cleaned) {
			return nil, unrep("HEAD holds one allow-list for both providers")
		}
		if role != e.DefaultRole {
			return nil, unrep("HEAD holds one default role for both providers")
		}
	}
	joined, err := joinCSV(emails, unrep)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{oauthEnabled: json.RawMessage(`true`), oauthEmails: joined}
	for _, p := range oidcProviders {
		out[fmt.Sprintf(oauthClientIDFmt, p.id)] = mustJSON(ids[p.id])
	}
	if role != "" {
		out[oauthDefaultRole] = mustJSON(role)
	}
	return out, nil
}

// ---- the library roots ----

// booksLibraryRootID is the entry M6 makes of root_dir (05-library 5L.10;
// m6.BooksLibraryRoot, which this package cannot import).
const booksLibraryRootID = "books-library"

// headDerivedRoot reports whether id is a root M6 makes from HEAD's blob:
// books-library or import-<n> (m6.RootID's output, n canonical decimal). Only
// those hold HEAD's path_aliases.
func headDerivedRoot(id string) bool {
	if id == booksLibraryRootID {
		return true
	}
	rest, ok := strings.CutPrefix(id, "import-")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n >= 0 && strconv.Itoa(n) == rest
}

// UnfoldRoots is the inverse of the roots entries M6 stages: root_dir (the
// books-library entry's path, "" when there is none) and HEAD's path_aliases
// (a JSON list of {root, windows, unc, smb_url}). M6 attaches each HEAD alias
// to the root whose path is the alias's root, so the aliases come back from
// the entries of books-library and import-<n> (the roots HEAD's blob made),
// grouped by root in list order, each with the root's own path as its root. A
// root created after the cutover has no home in path_aliases.
//
// Lost on the way in, so not recoverable: an alias's own spelling of its root
// (a trailing slash comes back as the root's path), the order of aliases
// across roots, and an alias that matched no root (M6 quarantines it).
func UnfoldRoots(entries json.RawMessage) (rootDir string, aliases json.RawMessage, err error) {
	bad := func(what string) error { return fmt.Errorf("%w: library roots: %s", ErrConvertInput, what) }
	var raw []json.RawMessage
	if err := json.Unmarshal(entries, &raw); err != nil {
		return "", nil, bad("want a list of root entries")
	}
	type alias struct {
		Root    string `json:"root"`
		Windows string `json:"windows"`
		UNC     string `json:"unc"`
		SMBURL  string `json:"smb_url"`
	}
	out := []alias{}
	seen := false
	for _, r := range raw {
		var e struct {
			ID      string `json:"id"`
			Path    string `json:"path"`
			Aliases []struct {
				Windows string `json:"windows"`
				UNC     string `json:"unc"`
				SMBURL  string `json:"smb_url"`
			} `json:"aliases"`
		}
		if err := json.Unmarshal(r, &e); err != nil {
			return "", nil, bad("want a root entry object")
		}
		if e.ID == booksLibraryRootID {
			if seen {
				return "", nil, bad("the books-library root is listed twice")
			}
			seen = true
			rootDir = e.Path
		}
		if !headDerivedRoot(e.ID) {
			continue
		}
		for _, a := range e.Aliases {
			out = append(out, alias{Root: e.Path, Windows: a.Windows, UNC: a.UNC, SMBURL: a.SMBURL})
		}
	}
	return rootDir, mustJSON(out), nil
}
