// file: core/integrations/write.go
// version: 1.1.0
// guid: 29ac02ee-bf78-408e-a1cf-db1779640943
// last-edited: 2026-11-11

package integrations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The bodies of Put, Delete and Test (M1.3, M1.4, M1.5). Put and Delete run
// under the writer mutex; Test takes it only to read an adapter, never across
// its build or probe.

// shaped is a request's settings after the shared Put/Test rules.
type shaped struct {
	// settings is the stored layer: every non-secret field as it would be
	// stored (a locked field keeps its stored value), and no secret field.
	settings map[string]any
	// view is the effective layer the request would serve: settings with
	// the operator overlay applied.
	view map[string]any
	// clear holds the secret fields the request clears (an explicit null).
	clear map[string]bool
}

// shape applies the settings rules Put and Test share: every field is one of
// the adapter's; a secret field holds nil (clear), "" or its own secrets.Ref
// (keep), and its value travels in secretsIn only; a field an operator value
// locks may be omitted or repeat its effective value, and anything else is
// ErrFieldLocked; URL-typed fields are absolute URLs without userinfo; the
// effective settings validate against the adapter's schema. The effective
// values of the locked fields are read from ops through a's fields, not
// from cur's view, which holds no operator value while cur's adapter is
// unknown (an operator-only instance being created).
func shape(ref Ref, a *adapter, inst InstanceConfig, secretsIn map[string]secrets.Value, storedSettings map[string]any, cur *slot, ops map[string]config.OperatorValue) (shaped, error) {
	fs := a.fs
	out := shaped{settings: map[string]any{}, clear: map[string]bool{}}
	for _, f := range sortedFields(inst.Settings) {
		v := inst.Settings[f]
		p, known := fs.props[f]
		if !known {
			return shaped{}, invalidf("settings field %q is not a field of adapter %s", f, a.desc.meta.ID)
		}
		if p.secret {
			if err := checkSecretSetting(ref, f, v); err != nil {
				return shaped{}, err
			}
			if v == nil {
				out.clear[f] = true
			}
			continue
		}
		switch v.(type) {
		case secrets.Value, *secrets.Value, secrets.Ref, *secrets.Ref:
			return shaped{}, invalidf("settings field %q of %s is not a secret field and cannot hold a secret", f, ref)
		}
		out.settings[f] = v
	}
	for _, f := range sortedFields(secretsIn) {
		if !fs.isSecret(f) {
			return shaped{}, invalidf("secretsIn names %q, which is not a secret field of adapter %s", f, a.desc.meta.ID)
		}
	}
	var locked map[string]string
	if cur != nil {
		locked = cur.cfg.Locked
	}
	eff := overlayValues(fs, storedSettings, ops)
	for _, f := range sortedFields(locked) {
		if fs.isSecret(f) {
			if v, ok := secretsIn[f]; (ok && resupplied(v, secretName(ref, f))) || out.clear[f] {
				return shaped{}, fmt.Errorf("%w: %s field %s is set by %s", ErrFieldLocked, ref, f, locked[f])
			}
			continue
		}
		if v, ok := out.settings[f]; ok && !equalValue(v, eff[f]) {
			return shaped{}, fmt.Errorf("%w: %s field %s is set by %s", ErrFieldLocked, ref, f, locked[f])
		}
		if sv, ok := storedSettings[f]; ok {
			out.settings[f] = sv
		} else {
			delete(out.settings, f)
		}
	}
	for _, f := range fs.urls {
		if s, ok := out.settings[f].(string); ok && s != "" {
			if err := checkURL(f, s); err != nil {
				return shaped{}, err
			}
		}
	}
	out.view = maps.Clone(out.settings)
	for f := range locked {
		if v, ok := eff[f]; ok && !fs.isSecret(f) {
			out.view[f] = v
		}
	}
	if err := validateSettings(a.desc.meta.Settings, fs, out.view); err != nil {
		return shaped{}, err
	}
	return out, nil
}

// checkSecretSetting checks the Settings value of secret field f: nil, ""
// or the field's own secrets.Ref (or its decoded JSON form).
func checkSecretSetting(ref Ref, f string, v any) error {
	want := secretName(ref, f)
	switch sv := v.(type) {
	case nil:
		return nil
	case string:
		if sv == "" {
			return nil
		}
	case secrets.Ref:
		if sv.Name() == want {
			return nil
		}
	default:
		if name, ok := refName(v); ok && name == want {
			return nil
		}
	}
	return invalidf("secret field %q of %s holds its secrets.Ref (%s) or nothing; its value travels in secretsIn", f, ref, want)
}

// localFor applies the Local rule Put and Test share (SEC2-7): a writer that
// passes the ProtectAdmin check may set Local for the destinations it
// stores; anyone else may only keep a stored Local over unchanged
// destinations. It returns Local and the origin it is bound to.
func localFor(w writer, want bool, stored *StoredInstance, destChanged bool, newO map[string]string) (bool, string, error) {
	if !want {
		return false, "", nil
	}
	switch {
	case w.admin:
		return true, signature(newO), nil
	case stored != nil && stored.Malformed == "" && stored.Config.Local && !destChanged:
		return true, stored.LocalOrigin, nil
	}
	return false, "", fmt.Errorf("%w: setting Local, or keeping it across a destination change, needs core:admin and an interactive principal", ErrForbidden)
}

// adapterFor resolves the adapter a write names: inst.Adapter, else the
// stored row's, else the operator instance's. A stored instance's adapter
// cannot change (F7-01: delete and re-create), except while its row is
// malformed (the repair).
func (r *Registry) adapterFor(inst InstanceConfig, stored *StoredInstance) (*adapter, error) {
	id := inst.Adapter
	readable := stored != nil && stored.Malformed == ""
	if id == "" {
		switch {
		case readable:
			id = stored.Config.Adapter
		case stored == nil:
			id = r.o.OperatorInstances[inst.Ref]
		}
	}
	if readable && id != stored.Config.Adapter {
		return nil, invalidf("%s is an instance of %s; an instance's adapter does not change (delete it and create a new one)", inst.Ref, stored.Config.Adapter)
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	a := r.adapters[id]
	if a == nil {
		return nil, fmt.Errorf("%w: %s is not registered in this binary", ErrUnknownAdapter, id)
	}
	if a.desc.meta.Kind != inst.Ref.Kind {
		return nil, invalidf("adapter %s is of kind %s, not %s", id, a.desc.meta.Kind, inst.Ref.Kind)
	}
	return a, nil
}

// readableSettings is a stored row's settings, or nil for no row or a
// malformed one.
func readableSettings(stored *StoredInstance) map[string]any {
	if stored == nil || stored.Malformed != "" {
		return nil
	}
	return stored.Config.Settings
}

// putPlan is one validated Put, ready to stage.
type putPlan struct {
	row    StoredInstance
	writes []secrets.Write
	events []audit.Event
	// newCredentials is set when the Put writes a secret value: the human
	// action a ClassAuth hold waits for (M2.4). Clearing or dropping a
	// secret is not.
	newCredentials bool
}

// planPut validates inst against the snapshot and computes the row, the
// secret writes and the audit records of the Put. The caller holds r.mu.
func (r *Registry) planPut(ctx context.Context, w writer, inst InstanceConfig, secretsIn map[string]secrets.Value) (putPlan, error) {
	snap := r.snap.Load()
	if snap.storeErr != nil {
		return putPlan{}, fmt.Errorf("no write while the instance store cannot be read: %w", snap.storeErr)
	}
	ref := inst.Ref
	var stored *StoredInstance
	if row, ok := snap.rows[ref]; ok {
		stored = &row
	}
	a, err := r.adapterFor(inst, stored)
	if err != nil {
		return putPlan{}, err
	}
	var expect uint64
	if stored != nil {
		expect = stored.Config.Version
	}
	if inst.Version != expect {
		return putPlan{}, fmt.Errorf("%w: %s is at version %d, the write names %d", ErrVersionConflict, ref, expect, inst.Version)
	}
	if stored == nil {
		if err := r.checkCapacity(snap, ref, a); err != nil {
			return putPlan{}, err
		}
	}
	storedSettings := readableSettings(stored)
	ops := snap.ops[ref]
	sh, err := shape(ref, a, inst, secretsIn, storedSettings, snap.slots[ref], ops)
	if err != nil {
		return putPlan{}, err
	}
	fs := a.fs
	keys := fs.destinationKeys()
	// The destinations before and after, from the effective views (the
	// stored layer with the operator overlay applied): a port change beside
	// an operator-set host is a destination change.
	oldO, newO := fs.origins(overlayValues(fs, storedSettings, ops)), fs.origins(sh.view)
	readable := stored != nil && stored.Malformed == ""
	destChanged := readable && !sameOrigins(keys, oldO, newO)
	keepStored := readable && !destChanged && (stored.SecretsOrigin == "" || stored.SecretsOrigin == signature(oldO))

	var p putPlan
	target := instanceEntity(ref)
	by := w.subject()
	dropped := false
	for _, f := range fs.secrets {
		name := secretName(ref, f)
		if v, ok := secretsIn[f]; ok && resupplied(v, name) {
			p.writes = append(p.writes, secrets.Write{Name: name, Value: v, By: by})
			p.events = append(p.events, audit.Event{
				Action: "core.integration.write_secret", Outcome: audit.Success, Target: target,
				Details: map[string]any{"instance": ref.String(), "field": f},
			})
			sh.settings[f] = secrets.RefTo(name)
			p.newCredentials = true
			continue
		}
		sr, isRef := storedSettings[f].(secrets.Ref)
		had := isRef && sr.Name() == name
		if keepStored && had && !sh.clear[f] {
			sh.settings[f] = secrets.RefTo(name)
			continue
		}
		if had || !readable {
			// A record may sit under the name: the field's old secret, or
			// one a deleted instance of this ID left. It is deleted, never
			// revived.
			stale, err := r.staleRecord(ctx, name)
			if err != nil {
				return putPlan{}, err
			}
			if stale {
				p.writes = append(p.writes, secrets.Write{Name: name, Delete: true, By: by})
			}
		}
		// A stored secret the write does not keep is dropped, unless the
		// request itself cleared it over unchanged destinations.
		if had && (destChanged || !sh.clear[f]) {
			dropped = true
		}
	}
	local, localOrigin, err := localFor(w, inst.Local, stored, destChanged, newO)
	if err != nil {
		return putPlan{}, err
	}
	var oso map[string]string
	switch {
	case w.admin && len(newO) > 0:
		oso = maps.Clone(newO)
	case !w.admin && readable:
		oso = maps.Clone(stored.OperatorSecretOrigin)
		for _, k := range keys {
			if oldO[k] != newO[k] {
				delete(oso, k)
			}
		}
	}
	p.row = StoredInstance{
		Config: InstanceConfig{
			Ref: ref, Adapter: a.desc.meta.ID, Name: inst.Name, Enabled: inst.Enabled, Priority: inst.Priority,
			Origin: w.origin(), Local: local, Settings: sh.settings, Limits: inst.Limits,
			Version: expect, Updated: r.now().UTC(),
		},
		LocalOrigin: localOrigin, SecretsOrigin: signature(newO), OperatorSecretOrigin: oso,
	}
	details := map[string]any{"instance": ref.String(), "adapter": string(a.desc.meta.ID), "origin": string(w.origin()), "local": local}
	ev := audit.Event{Action: "core.integration.update", Outcome: audit.Success, Target: target, Details: details}
	switch {
	case stored == nil:
		ev.Action = "core.integration.create"
	case destChanged:
		ev.Action = "core.integration.change_url"
		details["destinations"] = signature(newO)
		details["previous_destinations"] = signature(oldO)
	}
	if dropped {
		// The fact is the record's Reason: a detail key naming secrets would
		// be masked by the audit redactor.
		ev.Reason = "secrets_dropped"
	}
	p.events = append([]audit.Event{ev}, p.events...)
	return p, nil
}

// checkCapacity refuses a create past core.integrations.max_instances_per_kind
// or the adapter's Meta.MaxInstances.
func (r *Registry) checkCapacity(snap *snapshot, ref Ref, a *adapter) error {
	kind, same := 0, 0
	for rf, row := range snap.rows {
		if rf.Kind != ref.Kind {
			continue
		}
		kind++
		if row.Config.Adapter == a.desc.meta.ID {
			same++
		}
	}
	if limit := r.settings().MaxInstancesPerKind; kind >= limit {
		return invalidf("kind %s already has %d instances (core.integrations.max_instances_per_kind)", ref.Kind, limit)
	}
	if m := a.desc.meta.MaxInstances; m > 0 && same >= m {
		return invalidf("adapter %s allows %d instances", a.desc.meta.ID, m)
	}
	return nil
}

// staleRecord reports whether a stored secret record (set, or unreadable)
// sits under name. A name an operator value supplies to core secrets is not
// a stored record and cannot be written here.
func (r *Registry) staleRecord(ctx context.Context, name secrets.Name) (bool, error) {
	m, err := r.o.Secrets.Meta(ctx, name)
	if err != nil {
		return false, fmt.Errorf("%w: read secret %s: %w", ErrUnavailable, name, err)
	}
	return m.Source == secrets.SourceStored && (m.Set || m.Err != nil), nil
}

// commitPut stages p in one batch, commits it and swaps the instance. The
// caller holds r.mu.
func (r *Registry) commitPut(ctx context.Context, p putPlan) (err error) {
	ref := p.row.Config.Ref
	b := r.o.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	v, err := r.o.Store.Put(ctx, b, p.row)
	if err != nil {
		return storeErr(ref, err)
	}
	if err := r.stage(ctx, b, p.writes, p.events); err != nil {
		return err
	}
	if err := commit(ctx, b, ref); err != nil {
		return err
	}
	row := p.row
	row.Config.Version = v
	row.Config.Stored = true
	r.applyRow(ref, &row)
	if p.newCredentials && r.o.ReleaseAuthHold != nil {
		if err := r.o.ReleaseAuthHold(ctx, ref); err != nil {
			r.log.Warn("integrations: new credentials were stored but the auth hold was not released", "instance", ref.String(), "error", err)
		}
	}
	return nil
}

// stage stages secret writes and audit records into b.
func (r *Registry) stage(ctx context.Context, b *storage.Batch, writes []secrets.Write, events []audit.Event) error {
	for _, wr := range writes {
		if err := r.o.Secrets.Stage(ctx, b, wr); err != nil {
			return fmt.Errorf("%w: stage secret %s: %w", ErrUnavailable, wr.Name, err)
		}
	}
	for _, e := range events {
		if err := r.o.Audit.RecordIn(ctx, b, e); err != nil {
			return fmt.Errorf("%w: audit %s: %w", ErrUnavailable, e.Action, err)
		}
	}
	return nil
}

// storeErr maps an instance-store error: a version conflict, a refused
// record or an absent row stays as it is; anything else is the store being
// unavailable.
func storeErr(ref Ref, err error) error {
	if errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotConfigured) {
		return err
	}
	return fmt.Errorf("%w: write %s: %w", ErrUnavailable, ref, err)
}

// commit commits b; a failed precondition is another writer having
// committed first.
func commit(ctx context.Context, b *storage.Batch, ref Ref) error {
	err := b.Commit(ctx, storage.Sync)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrConflict):
		return fmt.Errorf("%w: another writer changed %s first: %w", ErrVersionConflict, ref, err)
	}
	return fmt.Errorf("%w: commit %s: %w", ErrUnavailable, ref, err)
}

// applyRow publishes a snapshot holding row as ref's stored row (nil: no
// row) and recomputes ref's slot. The caller holds r.mu.
func (r *Registry) applyRow(ref Ref, row *StoredInstance) {
	cur := r.snap.Load()
	next := &snapshot{storeErr: cur.storeErr, rows: maps.Clone(cur.rows), ops: cur.ops, slots: maps.Clone(cur.slots)}
	if row == nil {
		delete(next.rows, ref)
	} else {
		next.rows[ref] = *row
	}
	switch {
	case row == nil && next.ops[ref] == nil:
		delete(next.slots, ref)
	default:
		s := r.effective(ref, row, next.ops[ref])
		if old := cur.slots[ref]; old != nil && old.fp == s.fp {
			s = old
		}
		next.slots[ref] = s
	}
	r.publish(next, cur)
}

// delete removes ref's row and its secrets in one batch. The caller holds
// r.mu.
func (r *Registry) delete(ctx context.Context, w writer, ref Ref) (err error) {
	snap := r.snap.Load()
	if snap.storeErr != nil {
		return fmt.Errorf("no write while the instance store cannot be read: %w", snap.storeErr)
	}
	stored, ok := snap.rows[ref]
	if !ok {
		if snap.slots[ref] != nil {
			return fmt.Errorf("%w: %s exists only through operator values; remove them from the operator layer", ErrFieldLocked, ref)
		}
		return fmt.Errorf("%w: %s", ErrNotConfigured, ref)
	}
	var names []secrets.Name
	if a := r.adapters[stored.Config.Adapter]; a != nil && stored.Malformed == "" {
		for _, f := range a.fs.secrets {
			names = append(names, secretName(ref, f))
		}
	} else {
		prefix := string(secretName(ref, ""))
		for _, f := range sortedFields(stored.Config.Settings) {
			if sr, ok := stored.Config.Settings[f].(secrets.Ref); ok && strings.HasPrefix(string(sr.Name()), prefix) {
				names = append(names, sr.Name())
			}
		}
	}
	var writes []secrets.Write
	for _, n := range names {
		stale, err := r.staleRecord(ctx, n)
		if err != nil {
			return err
		}
		if stale {
			writes = append(writes, secrets.Write{Name: n, Delete: true, By: w.subject()})
		}
	}
	b := r.o.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := r.o.Store.Delete(ctx, b, ref); err != nil {
		return storeErr(ref, err)
	}
	ev := audit.Event{
		Action: "core.integration.delete", Outcome: audit.Success, Target: instanceEntity(ref),
		Details: map[string]any{"instance": ref.String(), "adapter": string(stored.Config.Adapter)},
	}
	if err := r.stage(ctx, b, writes, []audit.Event{ev}); err != nil {
		return err
	}
	if err := commit(ctx, b, ref); err != nil {
		return err
	}
	r.applyRow(ref, nil)
	return nil
}

// test is Test's body: it builds a throwaway slot as the Put would leave
// the instance, probes it and discards it. Nothing is persisted.
func (r *Registry) test(ctx context.Context, w writer, inst InstanceConfig, secretsIn map[string]secrets.Value) (TestResult, error) {
	ref := inst.Ref
	if !ref.Kind.Valid() {
		return TestResult{}, ref.Validate()
	}
	snap := r.snap.Load()
	var stored *StoredInstance
	var cur *slot
	var ops map[string]config.OperatorValue
	if ref.Instance != "" {
		if err := ref.Instance.Validate(); err != nil {
			return TestResult{}, err
		}
		if row, ok := snap.rows[ref]; ok && snap.storeErr == nil {
			stored = &row
		}
		cur, ops = snap.slots[ref], snap.ops[ref]
	}
	r.mu.Lock()
	a, err := r.adapterFor(inst, stored)
	r.mu.Unlock()
	if err != nil {
		return TestResult{}, err
	}
	storedSettings := readableSettings(stored)
	sh, err := shape(ref, a, inst, secretsIn, storedSettings, cur, ops)
	if err != nil {
		return TestResult{}, err
	}
	fs := a.fs
	oldO, newO := fs.origins(overlayValues(fs, storedSettings, ops)), fs.origins(sh.view)
	readable := stored != nil && stored.Malformed == ""
	destChanged := readable && !sameOrigins(fs.destinationKeys(), oldO, newO)
	local, localOrigin, err := localFor(w, inst.Local, stored, destChanged, newO)
	if err != nil {
		return TestResult{}, err
	}
	sameDest := readable && !destChanged && (stored.SecretsOrigin == "" || stored.SecretsOrigin == signature(oldO))
	given := map[string]secrets.Value{}
	for _, f := range fs.secrets {
		name := secretName(ref, f)
		if v, ok := secretsIn[f]; ok && resupplied(v, name) {
			given[f] = v
			sh.settings[f] = secrets.RefTo(name)
			continue
		}
		if sr, ok := storedSettings[f].(secrets.Ref); ok && sr.Name() == name && sameDest && !sh.clear[f] {
			sh.settings[f] = secrets.RefTo(name)
		}
	}
	row := StoredInstance{
		Config: InstanceConfig{
			Ref: ref, Adapter: a.desc.meta.ID, Name: inst.Name, Enabled: true, Priority: inst.Priority,
			Origin: w.origin(), Local: local, Settings: sh.settings, Limits: inst.Limits, Updated: r.now().UTC(),
		},
		LocalOrigin: localOrigin, SecretsOrigin: signature(newO),
	}
	if readable {
		row.OperatorSecretOrigin = stored.OperatorSecretOrigin
	}
	r.mu.Lock()
	s := r.effective(ref, &row, ops)
	r.mu.Unlock()
	s.storedSecrets = sameDest
	if sameDest {
		s.secretsOrigin = stored.SecretsOrigin
	}
	s.given = given
	if s.problem != "" {
		return TestResult{OK: false, Class: ClassInvalid}, nil
	}
	return r.runTest(ctx, s), nil
}

// runTest builds s, runs Health and the kind's extra test, and closes it.
// The result carries only OK, the Class and a status (SEC-15).
func (r *Registry) runTest(ctx context.Context, s *slot) TestResult {
	if msg := r.secretFinding(ctx, s); msg != "" {
		// The static findings Health reports come first: a required secret
		// the test has nothing to send for is the config's fault.
		c, _ := healthClass(HealthMisconfigured)
		return TestResult{Class: c}
	}
	inst, err := r.build(ctx, s)
	if err != nil {
		return TestResult{Class: buildClass(err)}
	}
	defer func() {
		if err := inst.Close(); err != nil {
			r.log.Warn("integrations: closing a tested instance failed", "instance", s.cfg.Ref.String(), "error", err)
		}
	}()
	pctx, cancel := context.WithTimeout(ctx, r.settings().ProbeTimeout)
	defer cancel()
	h := inst.Health(pctx)
	if c, failed := healthClass(h.Status); failed {
		return TestResult{Class: c}
	}
	if t, ok := inst.(Tester); ok {
		if err := t.Test(pctx); err != nil {
			res := TestResult{Class: ClassOf(ctx, err)}
			if e, ok := errors.AsType[*Error](err); ok {
				res.Status = e.Status
			}
			return res
		}
	}
	return TestResult{OK: true}
}

// healthClass maps a failed health status to the Class a TestResult
// reports; OK and Degraded (reachable) are not failures.
func healthClass(s HealthStatus) (Class, bool) {
	switch s {
	case HealthOK, HealthDegraded:
		return ClassUnknown, false
	case HealthDown:
		return ClassTransient, true
	case HealthAuthFailed:
		return ClassAuth, true
	case HealthMisconfigured:
		return ClassInvalid, true
	case HealthDisabled:
		return ClassRefusedLocal, true
	}
	return ClassUnknown, true
}

// buildClass classifies a build failure: the adapter's own *Error gives
// its Class; anything else is the settings' fault (a build does no I/O,
// M1.4), never the Registry's wrapping of it.
func buildClass(err error) Class {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Class
	}
	return ClassInvalid
}

// sinceOrZero is the time elapsed since start, at least 1ns once started.
func sinceOrZero(now func() time.Time, start time.Time) time.Duration {
	d := now().Sub(start)
	if d <= 0 {
		return time.Nanosecond
	}
	return d
}
