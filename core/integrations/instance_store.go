// file: core/integrations/instance_store.go
// version: 1.0.1
// guid: 2141de3b-d072-48fc-a0f7-b1f628eb04a5
// last-edited: 2026-11-11

package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
)

// ---- persistence (R4-1) --------------------------------------------------

// InstanceStore persists instance records. It is SDK surface, frozen by the
// API-stability gate. Core ships its one implementation, NativeInstanceStore
// (M1.4 "Native store"). Every write stages into the caller's batch: the
// Registry stages the instance's secrets (core secrets) and its audit record
// in that same batch.
//
// integrationstest.InstanceStoreSuite is its contract. The version rules
// every implementation keeps: Put of Version 0 creates and fails with
// ErrVersionConflict when a row exists; Put of Version n updates the row at
// version n and fails with ErrVersionConflict otherwise; the new version is
// n+1. A malformed row (Load returns it with Malformed set) is repaired by a
// Put naming the Version Load reported for it: its stored version when that
// can still be read, else 0. Delete of an absent row is ErrNotConfigured. A
// failed Put or Delete stages nothing.
//
// The version counts writes within one life of a row, not across a delete:
// a re-created row starts again at version 1, so a writer still holding
// version 1 of the deleted row can overwrite the new one (ABA). The check
// guards concurrent edits of one instance; a client that must not write
// over a re-created instance compares more than the version.
type InstanceStore interface {
	Load(ctx context.Context) ([]StoredInstance, error)
	// Put writes inst and returns the new Version.
	Put(ctx context.Context, b *storage.Batch, inst StoredInstance) (uint64, error)
	Delete(ctx context.Context, b *storage.Batch, ref Ref) error
}

// StoredInstance is one persisted record: InstanceConfig without the
// effective-view fields, plus the trust fields of the row. The instance store
// never reads or writes a secret value: the Registry reads and stages every
// secret through secrets, and the row holds only secrets.Refs.
type StoredInstance struct {
	Config        InstanceConfig // Locked and Stored are ignored
	LocalOrigin   string         // scheme://host:port Local was granted for; "" = none
	SecretsOrigin string         // URL origin the core secrets copies were written for
	// OperatorSecretOrigin maps each destination field (a URL-typed field, or
	// "host" for a host-and-port adapter such as core.email, whose origin is
	// smtp://host:port) to the origin an operator-sourced secret may be sent to
	// although the operator did not set that field (M1.4 step 2, R8-5,
	// SEC8-2). Written only by a Put that passes the ProtectAdmin check, or by
	// an app's cutover step (M1.10); a Put by anyone else that changes a
	// field's origin deletes that field's entry. nil = none.
	OperatorSecretOrigin map[string]string
	// Malformed is non-empty when the stored value cannot be decoded; it names
	// the field. The instance is HealthMisconfigured and nothing is rewritten.
	Malformed string
}

// NativeInstanceStore is core's implementation over its own keyspace
// c/integrations/ (M1.4 "Native store").
func NativeInstanceStore(ks storage.Keyspace) InstanceStore {
	return nativeStore{s: instancestore.New(ks, rowVersion)}
}

// nativeStore is NativeInstanceStore over core/internal/instancestore.
type nativeStore struct{ s *instancestore.Store }

// Load decodes every row. A row that does not decode is returned with
// Malformed set and its Ref from its key; nothing is rewritten.
func (n nativeStore) Load(ctx context.Context) ([]StoredInstance, error) {
	rows, err := n.s.Load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StoredInstance, 0, len(rows))
	for _, r := range rows {
		out = append(out, decodeRow(Ref{Kind: Kind(r.Kind), Instance: InstanceID(r.ID)}, r.Value))
	}
	return out, nil
}

// Put stages inst's row at its new version.
func (n nativeStore) Put(ctx context.Context, b *storage.Batch, inst StoredInstance) (uint64, error) {
	ref := inst.Config.Ref
	if err := ref.Validate(); err != nil {
		return 0, err
	}
	v, err := n.s.Put(ctx, b, string(ref.Kind), string(ref.Instance), inst.Config.Version, func(next uint64) ([]byte, error) {
		return encodeRow(inst, next)
	})
	if errors.Is(err, instancestore.ErrVersionMismatch) {
		return 0, fmt.Errorf("%w: %w", ErrVersionConflict, err)
	}
	return v, err
}

// Delete stages the deletion of ref's row.
func (n nativeStore) Delete(ctx context.Context, b *storage.Batch, ref Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	err := n.s.Delete(ctx, b, string(ref.Kind), string(ref.Instance))
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("%w: %s: %w", ErrNotConfigured, ref, err)
	}
	return err
}

// ---- the row (codec.JSON[integrations.StoredInstance](1)) -----------------

// rowJSON is a row's stored form: every field of StoredInstance but the
// effective-view ones (Locked, Stored) and Malformed.
type rowJSON struct {
	Kind                 Kind              `json:"kind"`
	Instance             InstanceID        `json:"instance"`
	Adapter              AdapterID         `json:"adapter"`
	Name                 string            `json:"name,omitempty"`
	Enabled              bool              `json:"enabled"`
	Priority             int               `json:"priority,omitzero"`
	Origin               Origin            `json:"origin"`
	Local                bool              `json:"local,omitzero"`
	Settings             map[string]any    `json:"settings,omitempty"`
	Limits               *limitsJSON       `json:"limits,omitempty"`
	Version              uint64            `json:"version"`
	Updated              time.Time         `json:"updated"`
	LocalOrigin          string            `json:"local_origin,omitempty"`
	SecretsOrigin        string            `json:"secrets_origin,omitempty"`
	OperatorSecretOrigin map[string]string `json:"operator_secret_origin,omitempty"`
}

// limitsJSON is Limits' stored form (time.Location has no JSON form; a
// class-keyed map is keyed by the class's number).
type limitsJSON struct {
	RPS           float64       `json:"rps,omitzero"`
	Burst         int           `json:"burst,omitzero"`
	MaxRetries    int           `json:"max_retries,omitzero"`
	Timeout       time.Duration `json:"timeout,omitzero"`
	RetryAfterMax time.Duration `json:"retry_after_max,omitzero"`
	Daily         *dailyJSON    `json:"daily,omitempty"`
	Breaker       BreakerPolicy `json:"breaker,omitzero"`
	Holds         holdsJSON     `json:"holds,omitzero"`
}

type dailyJSON struct {
	Total      int      `json:"total,omitzero"`
	Background int      `json:"background,omitzero"`
	Zone       string   `json:"zone,omitempty"`
	Counters   []string `json:"counters,omitempty"`
}

type holdsJSON struct {
	Durations map[string]time.Duration `json:"durations,omitempty"`
	Max       time.Duration            `json:"max,omitzero"`
	Persist   bool                     `json:"persist,omitzero"`
}

// rowCodec is the stored form of a row (M1.4 "Native store").
func rowCodec() codec.Codec[rowJSON] { return codec.JSON[rowJSON](1) }

// toLimitsJSON converts l for storage.
func toLimitsJSON(l *Limits) *limitsJSON {
	if l == nil {
		return nil
	}
	out := &limitsJSON{
		RPS: l.RPS, Burst: l.Burst, MaxRetries: l.MaxRetries, Timeout: l.Timeout,
		RetryAfterMax: l.RetryAfterMax, Breaker: l.Breaker,
		Holds: holdsJSON{Max: l.Holds.Max, Persist: l.Holds.Persist},
	}
	if l.Daily != nil {
		d := &dailyJSON{Total: l.Daily.Total, Background: l.Daily.Background, Counters: l.Daily.Counters}
		if l.Daily.Zone != nil {
			d.Zone = l.Daily.Zone.String()
		}
		out.Daily = d
	}
	if len(l.Holds.Durations) > 0 {
		out.Holds.Durations = make(map[string]time.Duration, len(l.Holds.Durations))
		for c, d := range l.Holds.Durations {
			out.Holds.Durations[strconv.Itoa(int(c))] = d
		}
	}
	return out
}

// fromLimitsJSON converts a stored limits record; an unknown zone or class
// key is an error (the row is malformed).
func fromLimitsJSON(j *limitsJSON) (*Limits, error) {
	if j == nil {
		return nil, nil
	}
	l := &Limits{
		RPS: j.RPS, Burst: j.Burst, MaxRetries: j.MaxRetries, Timeout: j.Timeout,
		RetryAfterMax: j.RetryAfterMax, Breaker: j.Breaker,
		Holds: HoldPolicy{Max: j.Holds.Max, Persist: j.Holds.Persist},
	}
	if j.Daily != nil {
		d := &DailyLimits{Total: j.Daily.Total, Background: j.Daily.Background, Counters: j.Daily.Counters}
		if j.Daily.Zone != "" {
			z, err := time.LoadLocation(j.Daily.Zone)
			if err != nil {
				return nil, fmt.Errorf("daily zone %q: %w", j.Daily.Zone, err)
			}
			d.Zone = z
		}
		l.Daily = d
	}
	if len(j.Holds.Durations) > 0 {
		l.Holds.Durations = make(map[Class]time.Duration, len(j.Holds.Durations))
		for k, d := range j.Holds.Durations {
			n, err := strconv.ParseUint(k, 10, 8)
			if err != nil {
				return nil, fmt.Errorf("hold class key %q: %w", k, err)
			}
			l.Holds.Durations[Class(n)] = d
		}
	}
	return l, nil
}

// encodeRow renders inst at version v. A settings value that cannot be
// stored (a secrets.Value refuses to marshal) is an error: the row never
// holds a secret.
func encodeRow(inst StoredInstance, v uint64) ([]byte, error) {
	c := inst.Config
	row := rowJSON{
		Kind: c.Ref.Kind, Instance: c.Ref.Instance, Adapter: c.Adapter, Name: c.Name,
		Enabled: c.Enabled, Priority: c.Priority, Origin: c.Origin, Local: c.Local,
		Settings: c.Settings, Limits: toLimitsJSON(c.Limits), Version: v, Updated: c.Updated.UTC(),
		LocalOrigin: inst.LocalOrigin, SecretsOrigin: inst.SecretsOrigin,
		OperatorSecretOrigin: inst.OperatorSecretOrigin,
	}
	if len(row.OperatorSecretOrigin) == 0 {
		row.OperatorSecretOrigin = nil
	}
	b, err := rowCodec().Encode(row)
	if err != nil {
		return nil, fmt.Errorf("integrations: encode %s: %w", c.Ref, err)
	}
	return b, nil
}

// rowVersion reads a stored row's version.
func rowVersion(raw []byte) (uint64, error) {
	r, err := codec.JSON[struct {
		Version uint64 `json:"version"`
	}](1).Decode(raw)
	if err != nil {
		return 0, err
	}
	return r.Version, nil
}

// decodeRow decodes ref's row. A row that does not decode, or whose kind or
// instance is not its key's, is returned with Malformed naming the first
// field that fails ("row" when the value is not a row at all).
func decodeRow(ref Ref, raw []byte) StoredInstance {
	bad := func(field string) StoredInstance {
		si := StoredInstance{Config: InstanceConfig{Ref: ref, Stored: true}, Malformed: field}
		if v, err := rowVersion(raw); err == nil {
			si.Config.Version = v
		}
		return si
	}
	row, err := rowCodec().Decode(raw)
	if err != nil {
		return bad(malformedField(raw))
	}
	if row.Kind != ref.Kind || row.Instance != ref.Instance {
		return bad("ref")
	}
	lim, err := fromLimitsJSON(row.Limits)
	if err != nil {
		return bad("limits")
	}
	return StoredInstance{
		Config: InstanceConfig{
			Ref: ref, Adapter: row.Adapter, Name: row.Name, Enabled: row.Enabled, Priority: row.Priority,
			Origin: row.Origin, Local: row.Local, Settings: refsIn(row.Settings), Limits: lim,
			Version: row.Version, Updated: row.Updated, Stored: true,
		},
		LocalOrigin: row.LocalOrigin, SecretsOrigin: row.SecretsOrigin,
		OperatorSecretOrigin: row.OperatorSecretOrigin,
	}
}

// malformedField names the first member of a row that does not decode into
// its field, or "row" when raw is not a versioned JSON object at all.
func malformedField(raw []byte) string {
	members, err := codec.JSON[map[string]json.RawMessage](1).Decode(raw)
	if err != nil {
		return "row"
	}
	probe := map[string]any{
		"kind": new(Kind), "instance": new(InstanceID), "adapter": new(AdapterID), "name": new(string),
		"enabled": new(bool), "priority": new(int), "origin": new(Origin), "local": new(bool),
		"settings": new(map[string]any), "limits": new(limitsJSON), "version": new(uint64),
		"updated": new(time.Time), "local_origin": new(string), "secrets_origin": new(string),
		"operator_secret_origin": new(map[string]string),
	}
	for _, k := range slices.Sorted(maps.Keys(members)) {
		dst, known := probe[k]
		if !known {
			return k
		}
		dec := json.NewDecoder(bytes.NewReader(members[k]))
		if dec.Decode(dst) != nil {
			return k
		}
	}
	return "row"
}

// refsIn returns settings with every {"secret": "<name>"} value decoded as an
// unbound secrets.Ref, the stored form of a secret field.
func refsIn(settings map[string]any) map[string]any {
	if settings == nil {
		return nil
	}
	out := maps.Clone(settings)
	for k, v := range out {
		if name, ok := refName(v); ok {
			out[k] = secrets.RefTo(name)
		}
	}
	return out
}

// refName reports whether v is a Ref's decoded JSON form {"secret": "<name>"}
// and returns the name.
func refName(v any) (secrets.Name, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	s, ok := m["secret"].(string)
	if !ok {
		return "", false
	}
	return secrets.Name(s), true
}
