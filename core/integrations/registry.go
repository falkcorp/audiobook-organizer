// file: core/integrations/registry.go
// version: 1.2.0
// guid: 5291a2b4-f665-40ef-8c7f-d588aa319bec
// last-edited: 2026-11-11

package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// managePerm is the permission every write of the Registry requires.
const managePerm rbac.Permission = "core:integrations.manage"

// Settings is the live core.integrations section the Registry reads on each
// call (05-metadata M1.7; name coined by F7-01). The kernel adapts its
// config section to it; zero fields take M1.7's defaults.
type Settings struct {
	Disabled            bool          // core.integrations.disabled
	DrainTimeout        time.Duration // core.integrations.drain_timeout; default 30s
	ProbeTimeout        time.Duration // core.integrations.probe_timeout; default 10s
	HealthParallelism   int           // core.integrations.health_parallelism; default 4
	MaxInstancesPerKind int           // core.integrations.max_instances_per_kind; default 32
}

// withDefaults fills M1.7's defaults.
func (s Settings) withDefaults() Settings {
	if s.DrainTimeout <= 0 {
		s.DrainTimeout = 30 * time.Second
	}
	if s.ProbeTimeout <= 0 {
		s.ProbeTimeout = 10 * time.Second
	}
	if s.HealthParallelism < 1 {
		s.HealthParallelism = 4
	}
	if s.MaxInstancesPerKind < 1 {
		s.MaxInstancesPerKind = 32
	}
	return s
}

// SecretStore is what the Registry needs of core secrets
// (secrets.(*Store)): reads, and a write staged into the caller's batch
// (name coined by F7-01).
type SecretStore interface {
	Get(ctx context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error)
	Meta(ctx context.Context, n secrets.Name) (secrets.Meta, error)
	Stage(ctx context.Context, b *storage.Batch, w secrets.Write) error
}

// Trust is what providerhttp's egress guard (M2.4) needs from the Registry
// for one instance (name coined by F7-01): the origins of its configured
// destinations that may resolve to private addresses. An origin is listed
// when its effective value came from an operator layer (M1.4 step 2: the
// operator layer carries lan_allow's authority), or when the instance's
// Local holds for the stored origin it was granted for. Every other host
// reaches public addresses and core.integrations.egress.lan_allow only.
type Trust struct {
	PrivateOrigins []string
}

// OutboundSpec is what Options.Outbound builds one instance's client and
// gate from (name coined by F7-01; providerhttp's Registry at F7-02).
type OutboundSpec struct {
	Ref      Ref
	Adapter  AdapterID
	Egress   EgressPolicy
	Limits   Limits  // the adapter's Meta.Limits
	Override *Limits // the instance's own (zero fields inherit)
	Trust    Trust
}

// Options configures New (name coined by F7-01: M1.3 declares Registry
// "per-App" and leaves its construction to the kernel). Store, NewBatch,
// Secrets and Audit are required.
type Options struct {
	Store    InstanceStore         // NativeInstanceStore over c/integrations/
	NewBatch func() *storage.Batch // a batch Store, Secrets and Audit can join
	Secrets  SecretStore
	Audit    AuditRecorder
	// Outbound builds an instance's HTTP client and gate. nil gives every
	// instance a client and gate that refuse before sending
	// (ClassRefusedLocal): no outbound call at all.
	Outbound func(OutboundSpec) (HTTP, Gate)
	// ReleaseAuthHold releases ref's ClassAuth hold (providerhttp, M2.4);
	// called after a Put that wrote a new secret value commits (F7-01: a
	// cleared or dropped secret is not new credentials). nil = none.
	ReleaseAuthHold func(ctx context.Context, ref Ref) error
	// HealthChanged is called when a probe changes an instance's status:
	// the kernel publishes core.integration.health.changed.v1 from it
	// (M1.4 step 5). nil = none.
	HealthChanged func(ref Ref, h Health)
	// Settings reads the live core.integrations section; nil = defaults.
	Settings func() Settings
	// OperatorInstances names the adapter of each instance an operator
	// value may create with no stored row: the app's M1.10 table (ABO maps
	// its five operator instances there). An operator value for any other
	// unstored instance yields an instance reported "adapter not in this
	// binary".
	OperatorInstances map[Ref]AdapterID
	Metrics           *metrics.Scope // the core "integrations" scope; nil = none
	Logger            *slog.Logger
	Now               func() time.Time
}

// adapter is one registered adapter with its classified settings.
type adapter struct {
	desc Descriptor
	fs   fieldSet
}

// snapshot is the immutable instance map Lease reads without a lock
// (M1.6); writers swap it under the writer mutex.
type snapshot struct {
	storeErr error                                   // Load of the store failed: no stored instance is served
	rows     map[Ref]StoredInstance                  // the stored rows
	ops      map[Ref]map[string]config.OperatorValue // the operator overlay
	slots    map[Ref]*slot                           // the effective instances
}

// Registry is per-App. It owns descriptors, the stores, built instances and
// their swap/drain lifecycle.
type Registry struct {
	o   Options
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex // the writer mutex (M1.6): Register, Load, SetOverlay, Put, Delete, Close
	adapters map[AdapterID]*adapter
	loaded   bool
	snap     atomic.Pointer[snapshot]
	closed   atomic.Bool
	closers  sync.WaitGroup

	hmu  sync.Mutex
	last map[Ref]Health

	// secretsMu orders stored-secret reads against writes: a commit and
	// the publish of its row run write-held, secretFor's row check and
	// secret read run read-held (commitAndPublish).
	secretsMu sync.RWMutex

	met *instruments
}

// New builds a Registry. It does no I/O: Load reads the store.
func New(o Options) (*Registry, error) {
	var missing []string
	if o.Store == nil {
		missing = append(missing, "Store")
	}
	if o.NewBatch == nil {
		missing = append(missing, "NewBatch")
	}
	if o.Secrets == nil {
		missing = append(missing, "Secrets")
	}
	if o.Audit == nil {
		missing = append(missing, "Audit")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("integrations: New needs Options.%v", missing)
	}
	r := &Registry{o: o, log: o.Logger, now: o.Now, adapters: map[AdapterID]*adapter{}, last: map[Ref]Health{}}
	if r.log == nil {
		r.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if r.now == nil {
		r.now = time.Now
	}
	r.snap.Store(&snapshot{rows: map[Ref]StoredInstance{}, ops: map[Ref]map[string]config.OperatorValue{}, slots: map[Ref]*slot{}})
	if o.Metrics != nil {
		m, err := r.instrument(o.Metrics)
		if err != nil {
			return nil, err
		}
		r.met = m
	}
	return r, nil
}

// settings reads the live section.
func (r *Registry) settings() Settings {
	if r.o.Settings == nil {
		return Settings{}.withDefaults()
	}
	return r.o.Settings().withDefaults()
}

// Register adds descriptors registered by origin: "core" for the binary's
// opt-in core adapters (app.Options.Adapters), else the registering module's
// ID (Registrar.Adapter). Meta.Origin is set here. It fails (the kernel
// fails boot, M1.4 step 1) for an adapter ID outside origin's prefix, a
// duplicate ID, an unknown kind, a descriptor not built by NewDescriptor, a
// zero settings schema, or a secret-named field that is not a secret field.
// Register is valid before Load only.
func (r *Registry) Register(origin string, ds ...Descriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return errors.New("integrations: Register after Load; adapters are registered before listeners (M1.4 step 1)")
	}
	if !validOrigin(origin) {
		return fmt.Errorf("%w: registering origin %q is neither core nor a module ID", ErrInvalid, origin)
	}
	var errs []error
	for _, d := range ds {
		m := d.meta
		switch {
		case m.ID.Validate() != nil:
			errs = append(errs, m.ID.Validate())
			continue
		case m.ID.Prefix() != origin:
			errs = append(errs, fmt.Errorf("%w: adapter %s is registered by %q; an adapter ID carries its registering module's prefix, and core.* only comes from app.Options.Adapters", ErrInvalid, m.ID, origin))
			continue
		case !m.Kind.Valid():
			errs = append(errs, fmt.Errorf("%w: adapter %s: kind %q is not one of ai, download, indexer, metadata, notify", ErrInvalid, m.ID, m.Kind))
			continue
		case d.build == nil:
			errs = append(errs, fmt.Errorf("%w: adapter %s has no build func (descriptors come from NewDescriptor)", ErrInvalid, m.ID))
			continue
		case r.adapters[m.ID] != nil:
			errs = append(errs, fmt.Errorf("%w: adapter %s is registered twice", ErrInvalid, m.ID))
			continue
		}
		fs, err := analyze(m.Settings)
		if err != nil {
			errs = append(errs, fmt.Errorf("adapter %s: %w", m.ID, err))
			continue
		}
		d.meta.Origin = origin
		r.adapters[m.ID] = &adapter{desc: d, fs: fs}
	}
	return errors.Join(errs...)
}

// Load reads the instance store and applies the operator overlay (M1.4 step
// 2). It is called once, after Register. A store that cannot be read is
// open for boot and closed for every stored instance: none is served and
// each reports integration_store_unavailable, while operator-only instances
// are served (M1.5). Load returns the store error for the kernel to log; it
// never fails boot on it.
func (r *Registry) Load(ctx context.Context, overlay iter.Seq2[config.Path, config.OperatorValue]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return errors.New("integrations: Load runs once")
	}
	r.loaded = true
	next := &snapshot{rows: map[Ref]StoredInstance{}, ops: r.opPaths(overlay), slots: map[Ref]*slot{}}
	rows, err := r.o.Store.Load(ctx)
	if err != nil {
		next.storeErr = fmt.Errorf("%w: %w", ErrUnavailable, err)
		r.log.Error("integrations: the instance store cannot be read; no stored instance is served", "error", err)
	} else {
		for _, row := range rows {
			next.rows[row.Config.Ref] = row
		}
	}
	r.rebuild(next, nil)
	return next.storeErr
}

// SetOverlay replaces the operator overlay at a config hot swap (M1.4 steps
// 2 and 4). Only instances whose effective view changes are swapped; nothing
// is persisted. After Close it returns ErrClosed and swaps nothing.
func (r *Registry) SetOverlay(ctx context.Context, overlay iter.Seq2[config.Path, config.OperatorValue]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return ErrClosed
	}
	cur := r.snap.Load()
	next := &snapshot{storeErr: cur.storeErr, rows: cur.rows, ops: r.opPaths(overlay), slots: map[Ref]*slot{}}
	r.rebuild(next, cur)
	return nil
}

// rebuild computes next's slots, keeping every slot of prev whose
// fingerprint is unchanged, publishes next and retires the replaced slots.
// The caller holds r.mu.
func (r *Registry) rebuild(next, prev *snapshot) {
	refs := map[Ref]bool{}
	if next.storeErr == nil {
		for ref := range next.rows {
			refs[ref] = true
		}
	}
	for ref := range next.ops {
		refs[ref] = true
	}
	for ref := range refs {
		var row *StoredInstance
		if rw, ok := next.rows[ref]; ok && next.storeErr == nil {
			row = &rw
		}
		s := r.effective(ref, row, next.ops[ref])
		if prev != nil {
			if old := prev.slots[ref]; old != nil && old.fp == s.fp {
				s = old
			}
		}
		next.slots[ref] = s
	}
	r.publish(next, prev)
}

// publish stores next and retires every slot of prev it no longer holds.
// The caller holds r.mu.
func (r *Registry) publish(next, prev *snapshot) {
	r.snap.Store(next)
	r.forget(next.slots)
	if prev == nil {
		return
	}
	for ref, old := range prev.slots {
		if next.slots[ref] != old {
			r.retire(old)
			r.met.swap(ref)
		}
	}
}

// Adapters lists kind's registered adapters in ID order.
func (r *Registry) Adapters(kind Kind) iter.Seq[Meta] {
	r.mu.Lock()
	var metas []Meta
	for _, a := range r.adapters {
		if a.desc.meta.Kind == kind {
			metas = append(metas, a.desc.meta)
		}
	}
	r.mu.Unlock()
	slices.SortFunc(metas, func(a, b Meta) int { return compareStrings(string(a.ID), string(b.ID)) })
	return slices.Values(metas)
}

// Instances lists kind's effective instances (stored row + overlay) in ID
// order. While the store cannot be read only operator-only instances are
// listed.
func (r *Registry) Instances(kind Kind) iter.Seq[InstanceConfig] {
	snap := r.snap.Load()
	refs := make([]Ref, 0, len(snap.slots))
	for ref := range snap.slots {
		if ref.Kind == kind {
			refs = append(refs, ref)
		}
	}
	slices.SortFunc(refs, func(a, b Ref) int { return compareStrings(string(a.Instance), string(b.Instance)) })
	return func(yield func(InstanceConfig) bool) {
		for _, ref := range refs {
			if !yield(cloneConfig(snap.slots[ref].cfg)) {
				return
			}
		}
	}
}

// Config is ref's effective view.
func (r *Registry) Config(ref Ref) (InstanceConfig, bool) {
	s := r.snap.Load().slots[ref]
	if s == nil {
		return InstanceConfig{}, false
	}
	return cloneConfig(s.cfg), true
}

// Put validates and persists one instance with its secrets (one batch, M1.4).
// The principal in ctx sets Origin. URL-typed fields are written by any
// holder of core:integrations.manage (interactive or API key; SEC2-7). Put
// refuses (403) a principal without the ProtectAdmin check (core:admin +
// interactive) that sets Local, or keeps Local=true across a URL change; with
// authentication off the auth-disabled principal does not pass that check
// (05-rbac §4.11), so it cannot set Local either. A Put that changes a field
// in Locked is refused whole (409, ErrFieldLocked); one that omits the locked
// fields or repeats their effective value stores the other fields and keeps
// each locked field's stored value as it was. When ANY destination field of
// the instance changes (the scheme, host or port of a URL-typed settings
// field, or a host or port field of an adapter that dials a host and port,
// such as core.email's host and port, 05-notify §3.1; the destination fields
// of M1.4 step 2; lead decision, round 7), every secret field is cleared
// unless secretsIn re-supplies it, Local is reset to false unless the writer
// passes the ProtectAdmin check and sets it again, and the write is audited
// as core.integration.change_url with secrets_dropped=true (05-rbac §7.1;
// SEC-15). A secret counts as re-supplied only when the raw request carries a
// value that is neither empty nor an echoed or mask-shaped value (SEC3-4).
// An operator-sourced secret (M1.4 step 2) is never stored, so this clearing
// cannot reach it; it is bound to the destination by origin plus provenance
// instead: it is served only while every destination field's effective
// origin equals that field's operator origin (or the adapter default), or the
// instance's OperatorSecretOrigin, and is withheld otherwise (M1.4 step 2
// "Operator secrets bind to origin plus provenance"; SEC7-3, R8-5). A Put
// that passes the ProtectAdmin check records the destination origins it
// stores as OperatorSecretOrigin, so an admin's URL carries the env-supplied
// key; a Put by anyone else that changes a destination origin deletes that
// field's entry, so the key never follows a non-admin's URL. The Registry's
// writer mutex (M1.6) is held across the commit, never inside it.
// A Put that changes a secret field releases the instance's ClassAuth hold
// (M2.4): new credentials are the human action that hold waits for.
//
// F7-01's reading of the rules the spec leaves open (seams in the PR):
// a create stages the deletion of any secret record left under the new
// instance's names unless re-supplied, and a Delete deletes them, so a
// stale secret never revives (the rule #4577 applies to config's list
// entries); an adapter change of a stored instance is refused (delete and
// re-create); the auth-disabled principal writes with Origin "ui"; the
// secrets_dropped fact is the audit record's Reason, because the audit
// redactor masks any detail key containing "secret".
func (r *Registry) Put(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (InstanceConfig, error) {
	w, err := writerOf(ctx)
	if err != nil {
		return InstanceConfig{}, err
	}
	if err := inst.Ref.Validate(); err != nil {
		return InstanceConfig{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return InstanceConfig{}, ErrClosed
	}
	p, err := r.planPut(ctx, w, inst, secretsIn)
	if err != nil {
		return InstanceConfig{}, err
	}
	if err := r.commitPut(ctx, p); err != nil {
		return InstanceConfig{}, err
	}
	cfg, _ := r.Config(inst.Ref)
	return cfg, nil
}

// Delete removes ref's stored row and deletes the secrets of its secret
// fields in the same batch, audited as core.integration.delete. An
// operator-only instance has no row to delete (409, ErrFieldLocked); an
// unknown one is ErrNotConfigured. The operator overlay, if any, stays: the
// instance then serves as operator-only (Stored=false).
func (r *Registry) Delete(ctx context.Context, ref Ref) error {
	w, err := writerOf(ctx)
	if err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return ErrClosed
	}
	return r.delete(ctx, w, ref)
}

// Test builds a throwaway instance from an UNSAVED config, runs Health and
// the kind's extra test (caps fetch, login), and discards it. Nothing is
// persisted. The same egress guard as a saved instance applies; a config with
// Local=true needs the same ProtectAdmin check as Put (SEC2-7). TestResult
// carries only OK, the Class and the HTTP/protocol status: never a response
// body or remote error text (a test must not be a read oracle, SEC-15).
// The unscoped route (POST .../{kind}/test) has no stored instance, so only
// secretsIn is used. The instance-scoped route (.../instances/{id}/test) also
// takes an unsaved body for that instance (the edit form's "Test" before a
// save); a secret field the body omits is filled from the instance's stored
// secret only while every destination field's origin equals the stored one.
// When a destination origin differs, Test uses no stored secret: only a
// secretsIn value that passes Put's re-supply check (neither empty nor
// echoed nor mask-shaped), exactly as Put's destination-change clearing
// would leave it, so Test never sends a stored secret to a host no Put has
// stored (SEC8-10); and the stored Local applies to the changed destination
// only for a caller that passes the ProtectAdmin check, as Put would leave
// it. An operator-sourced secret follows M1.4 step 2's binding against the
// stored OperatorSecretOrigin; a Test never records provenance.
//
// The unscoped form is an inst whose Ref.Instance is empty (or names no
// stored instance): no stored secret can be used.
func (r *Registry) Test(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (TestResult, error) {
	w, err := writerOf(ctx)
	if err != nil {
		return TestResult{}, err
	}
	if r.closed.Load() {
		return TestResult{}, ErrClosed
	}
	if r.settings().Disabled {
		return TestResult{}, fmt.Errorf("%w: core.integrations.disabled is set", ErrDisabled)
	}
	return r.test(ctx, w, inst, secretsIn)
}

// Health is ref's health: the static findings (disabled, misconfigured, a
// missing or withheld secret) and, for a buildable instance, an active probe
// bounded by core.integrations.probe_timeout. While
// core.integrations.disabled is set it probes nothing.
func (r *Registry) Health(ctx context.Context, ref Ref) (Health, error) {
	s, snap := r.current(ref)
	if s == nil {
		switch {
		case r.closed.Load():
			return Health{}, ErrClosed
		case snap.storeErr != nil && ref.Validate() == nil:
			return Health{Status: HealthMisconfigured, Message: ReasonStoreUnavailable, CheckedAt: r.now()}, nil
		}
		return Health{}, fmt.Errorf("%w: %s", ErrNotConfigured, ref)
	}
	defer s.release()
	return r.probe(ctx, s), nil
}

// Lease returns the current built instance as T plus a release func; the
// instance cannot be Closed while a lease is held. Kind packages wrap this.
func Lease[T Instance](ctx context.Context, r *Registry, ref Ref) (T, func(), error) {
	var zero T
	inst, release, err := r.lease(ctx, ref)
	if err != nil {
		return zero, nil, err
	}
	v, ok := inst.(T)
	if !ok {
		release()
		return zero, nil, fmt.Errorf("%w: %s is built as %T, not the requested instance type", ErrNotConfigured, ref, inst)
	}
	return v, release, nil
}

// Close stops issuing leases, retires every instance and waits for their
// leases to drain and their Close to run, at most until ctx ends (the kernel
// bounds it with core.integrations.drain_timeout, M1.4 step 6).
func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed.Swap(true) {
		for _, s := range r.snap.Load().slots {
			r.retire(s)
		}
		if r.met != nil && r.met.gauges != nil {
			if err := r.met.gauges.Unregister(); err != nil {
				r.log.Warn("integrations: unregistering the gauges' callback failed", "error", err)
			}
		}
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() {
		r.closers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("integrations: instances still draining at shutdown: %w", ctx.Err())
	}
}

// ---- writers -------------------------------------------------------------

// writer is the principal a write acts as.
type writer struct {
	p     *auth.Principal
	admin bool // the ProtectAdmin check: core:admin and an interactive principal
}

// writerOf reads the principal from ctx: absent, or without
// core:integrations.manage, is ErrForbidden. ProtectAdmin is config's check
// (05-config §1.3.7): core:admin and Method.MayChangeCredentials, which is
// what config.Actor.Interactive carries (a delegated session counts, the
// owner's 2026-11-06 amendment; an API key and the auth-disabled principal
// never do).
func writerOf(ctx context.Context) (writer, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || p.Perms == nil {
		return writer{}, fmt.Errorf("%w: no authenticated principal", ErrForbidden)
	}
	if !p.Perms.Has(managePerm) {
		return writer{}, fmt.Errorf("%w: %s is required", ErrForbidden, managePerm)
	}
	return writer{p: p, admin: p.Method.MayChangeCredentials() && p.Perms.Has(rbac.CoreAdmin)}, nil
}

// origin is the Origin a write by w records: "api" for an API key, "ui" for
// every other principal (an interactive or delegated session, and the
// auth-disabled principal, which is the UI with authentication off).
func (w writer) origin() Origin {
	if w.p.Method == auth.MethodAPIKey {
		return OriginAPI
	}
	return OriginUI
}

// subject names w for secret writes (secrets.Write.By).
func (w writer) subject() string {
	switch {
	case w.p.User != nil && w.p.User.ID != "":
		return string(w.p.User.ID)
	case w.p.Method == auth.MethodAuthDisabled:
		return "auth_disabled"
	}
	return string(w.p.Method)
}

// instanceEntity is the audit target of an instance.
func instanceEntity(ref Ref) entity.Ref {
	return entity.Ref{Module: "core", Kind: "integration", ID: ref.String()}
}

// compareStrings orders two strings.
func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// sortedFields is m's keys in order.
func sortedFields[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
