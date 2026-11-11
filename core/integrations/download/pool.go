// file: core/integrations/download/pool.go
// version: 1.0.0
// guid: 7e3a9c15-8b04-4f62-a1d7-5c0e2b94f8a6
// last-edited: 2026-11-11

package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
)

// defaultCapsTTL is core.download.caps_ttl's default (05-download §7).
const defaultCapsTTL = time.Hour

// defaultParallelism bounds Relocate's fan-out over instances: the calls are
// network-bound, each to a different client, so the bound is small and fixed
// (the figure 05-download §4.5 gives a poll fan-out), not NumCPU.
const defaultParallelism = 4

// PoolOptions configures NewPool (a name coined by F7-04: 05-download §3 shows
// Pool as "per App" and leaves its construction to the kernel, which wires
// module.Deps.Downloads at F7-09).
type PoolOptions struct {
	// Paths maps client paths to host paths and back. nil maps nothing: every
	// item's LocalPath stays "" and Relocate is refused (fail closed).
	Paths PathMapper
	// CapsTTL is how long the Pool keeps an instance's Caps (core.download.caps_ttl,
	// default 1h). The key is not declared here; the kernel passes its value.
	CapsTTL time.Duration
	// Parallelism bounds Relocate's fan-out over instances (default 4).
	Parallelism int
	// Metrics is the core "download" scope, created with the labels adapter
	// and instance; nil records nothing.
	Metrics *metrics.Scope
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Logger receives the Pool's diagnostics (default: discarded).
	Logger *slog.Logger
}

// Pool is the per-App access point to every download instance (05-download
// §3): modules reach it as module.Deps.Downloads (F7-09). Its methods are safe
// for concurrent use.
type Pool struct {
	reg *integrations.Registry
	o   PoolOptions
	log *slog.Logger
	now func() time.Time

	unknownState metric.Int64Counter // nil without Metrics
	unmapped     metric.Int64Counter

	mu    sync.Mutex
	caps  map[integrations.Ref]capsEntry
	proto map[integrations.AdapterID]Protocol // learned from Caps
}

// capsEntry is one cached Caps; it is stale after the TTL or once the
// instance's stored row moved to another version or adapter.
type capsEntry struct {
	caps    Caps
	at      time.Time
	version uint64
	adapter integrations.AdapterID
}

// NewPool builds a Pool over reg. It does no I/O.
func NewPool(reg *integrations.Registry, o PoolOptions) (*Pool, error) {
	if reg == nil {
		return nil, errors.New("download: NewPool needs a Registry")
	}
	if o.CapsTTL <= 0 {
		o.CapsTTL = defaultCapsTTL
	}
	if o.Parallelism < 1 {
		o.Parallelism = defaultParallelism
	}
	p := &Pool{
		reg: reg, o: o, log: o.Logger, now: o.Now,
		caps: map[integrations.Ref]capsEntry{}, proto: map[integrations.AdapterID]Protocol{},
	}
	if p.log == nil {
		p.log = slog.New(slog.DiscardHandler)
	}
	if p.now == nil {
		p.now = time.Now
	}
	if o.Metrics != nil {
		m, prefix := o.Metrics.Meter(), o.Metrics.Prefix()
		var err error
		if p.unknownState, err = m.Int64Counter(prefix+".unknown_state",
			metric.WithDescription("Items whose client state no adapter table knows, reported as warning (05-download §4.2), by adapter.")); err != nil {
			return nil, fmt.Errorf("download: unknown state counter: %w", err)
		}
		if p.unmapped, err = m.Int64Counter(prefix+".unmapped_paths",
			metric.WithDescription("Client paths no mapping covers (05-download §4.4), by instance.")); err != nil {
			return nil, fmt.Errorf("download: unmapped paths counter: %w", err)
		}
	}
	return p, nil
}

// Instances lists the enabled download instances of proto whose adapter is in
// this binary, by Priority (lower first), then instance ID. A zero proto lists
// every protocol.
//
// An instance's protocol is its adapter's, which the Pool learns from the
// first Caps it fetches (Files and Relocate fetch it; the signature has no
// ctx to fetch it here). An instance whose adapter has not answered yet is
// listed for every protocol, so a client is never hidden until it was used;
// phase 1 has torrent adapters only, so the answer is exact there.
func (p *Pool) Instances(proto Protocol) []integrations.Ref {
	registered := map[integrations.AdapterID]bool{}
	for m := range p.reg.Adapters(integrations.KindDownload) {
		registered[m.ID] = true
	}
	var cfgs []integrations.InstanceConfig
	p.mu.Lock()
	for c := range p.reg.Instances(integrations.KindDownload) {
		if !c.Enabled || !registered[c.Adapter] {
			continue
		}
		if known := p.proto[c.Adapter]; proto != 0 && known != 0 && known != proto {
			continue
		}
		cfgs = append(cfgs, c)
	}
	p.mu.Unlock()
	slices.SortStableFunc(cfgs, func(a, b integrations.InstanceConfig) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(string(a.Ref.Instance), string(b.Ref.Instance))
	})
	refs := make([]integrations.Ref, len(cfgs))
	for i, c := range cfgs {
		refs[i] = c.Ref
	}
	return refs
}

// Lease returns ref's built Client and a release func; the instance is not
// closed while a lease is held. It is the Registry's Lease for the download
// kind: while core.integrations.disabled is set, or the instance is disabled,
// it fails closed (ErrDisabled, ClassRefusedLocal) before any client call.
func (p *Pool) Lease(ctx context.Context, ref integrations.Ref) (Client, func(), error) {
	if ref.Kind != integrations.KindDownload {
		return nil, nil, &integrations.Error{Ref: ref, Op: "lease", Class: integrations.ClassInvalid,
			Err: fmt.Errorf("%w: %s is not a download instance", integrations.ErrInvalid, ref)}
	}
	return integrations.Lease[Client](ctx, p.reg, ref)
}

// Items lists ref's items matching f, with LocalPath filled (05-download
// §4.4); an item no mapping covers keeps LocalPath "" and is counted in
// mediacore_download_unmapped_paths_total. Cross-instance discovery is the
// caller looping Instances; each call is one client.
func (p *Pool) Items(ctx context.Context, ref integrations.Ref, f ItemFilter) ([]Item, error) {
	c, release, err := p.Lease(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer release()
	items, err := c.Items(ctx, f)
	if err != nil {
		return nil, p.wrap(ctx, ref, "items", err)
	}
	adapter := p.adapterOf(ref)
	for i := range items {
		items[i].Ref.Instance = ref // an adapter never speaks for another instance
		if _, unknown := items[i].Raw[RawUnknownState]; unknown {
			p.count(ctx, p.unknownState, attribute.String("adapter", string(adapter)))
		}
		items[i].LocalPath = p.toLocal(ctx, ref, items[i].RemotePath)
	}
	return items, nil
}

// Files lists the files of item with LocalPath filled. The instance must have
// Caps.Files and implement FileLister, else ClassUnsupported; an item the
// client does not hold is ClassNotFound.
func (p *Pool) Files(ctx context.Context, item ItemRef) ([]File, error) {
	ref := item.Instance
	c, release, err := p.Lease(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := p.capable(ctx, ref, c, "files", func(c Caps) bool { return c.Files }, func(c Client) bool { _, ok := c.(FileLister); return ok }); err != nil {
		return nil, err
	}
	// File.Path is relative to the item's RemotePath, so the item is read
	// first: it gives the base the files map under, and says whether the
	// client holds the item at all.
	items, err := c.Items(ctx, ItemFilter{IDs: []ItemID{item.ID}})
	if err != nil {
		return nil, p.wrap(ctx, ref, "files", err)
	}
	at := slices.IndexFunc(items, func(it Item) bool { return it.Ref.ID == item.ID })
	if at < 0 {
		return nil, &integrations.Error{Ref: ref, Op: "files", Class: integrations.ClassNotFound,
			Err: fmt.Errorf("download: %s holds no item %q", ref, item.ID)}
	}
	files, err := c.(FileLister).Files(ctx, item.ID)
	if err != nil {
		return nil, p.wrap(ctx, ref, "files", err)
	}
	base := strings.TrimRight(items[at].RemotePath, `/\`)
	for i := range files {
		files[i].LocalPath = p.toLocal(ctx, ref, base+"/"+strings.TrimLeft(files[i].Path, `/\`))
	}
	return files, nil
}

// Relocate moves the data of items to localDir (a host path), grouping the
// refs by instance and mapping localDir to each client's own path. Nothing is
// sent anywhere until every instance accepted the call: an instance without
// Caps.Relocate or a Relocator is ClassUnsupported, and a localDir no mapping
// of an instance covers is ClassInvalid ("no mapping covers <path>"), never a
// host path the client cannot see. Instances are then called at most
// PoolOptions.Parallelism at a time; their errors are joined, the instance
// order being the order of their IDs.
func (p *Pool) Relocate(ctx context.Context, items []ItemRef, localDir LocalPath) error {
	if len(items) == 0 {
		return nil
	}
	groups := map[integrations.Ref][]ItemID{}
	for _, it := range items {
		if !slices.Contains(groups[it.Instance], it.ID) {
			groups[it.Instance] = append(groups[it.Instance], it.ID)
		}
	}
	refs := make([]integrations.Ref, 0, len(groups))
	for ref := range groups {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b integrations.Ref) int { return strings.Compare(a.String(), b.String()) })

	type job struct {
		ref       integrations.Ref
		c         Client
		remoteDir string
		ids       []ItemID
	}
	jobs := make([]job, 0, len(refs))
	for _, ref := range refs {
		c, release, err := p.Lease(ctx, ref)
		if err != nil {
			return err
		}
		defer release()
		if err := p.capable(ctx, ref, c, "relocate", func(c Caps) bool { return c.Relocate }, func(c Client) bool { _, ok := c.(Relocator); return ok }); err != nil {
			return err
		}
		remoteDir, err := p.toRemote(ctx, ref, localDir)
		if err != nil {
			return err
		}
		jobs = append(jobs, job{ref: ref, c: c, remoteDir: remoteDir, ids: groups[ref]})
	}

	errs := make([]error, len(jobs))
	sem := make(chan struct{}, p.o.Parallelism)
	var wg sync.WaitGroup
	for i, j := range jobs {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := j.c.(Relocator).SetLocation(ctx, j.ids, j.remoteDir); err != nil {
				errs[i] = p.wrap(ctx, j.ref, "relocate", err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// capable checks c for one optional capability: ClassUnsupported when its
// Caps do not claim it or it lacks the optional interface.
func (p *Pool) capable(ctx context.Context, ref integrations.Ref, c Client, op string, claims func(Caps) bool, has func(Client) bool) error {
	caps, err := p.capsOf(ctx, ref, c)
	if err != nil {
		return p.wrap(ctx, ref, op, err)
	}
	if !claims(caps) || !has(c) {
		return &integrations.Error{Ref: ref, Op: op, Class: integrations.ClassUnsupported,
			Err: fmt.Errorf("download: %s does not support %s", ref, op)}
	}
	return nil
}

// capsOf is c's Caps from the cache, or fetched and cached.
func (p *Pool) capsOf(ctx context.Context, ref integrations.Ref, c Client) (Caps, error) {
	cfg, _ := p.reg.Config(ref)
	now := p.now()
	p.mu.Lock()
	e, ok := p.caps[ref]
	p.mu.Unlock()
	if ok && now.Sub(e.at) < p.o.CapsTTL && e.version == cfg.Version && e.adapter == cfg.Adapter {
		return e.caps, nil
	}
	caps, err := c.Caps(ctx)
	if err != nil {
		return Caps{}, err
	}
	p.mu.Lock()
	p.caps[ref] = capsEntry{caps: caps, at: now, version: cfg.Version, adapter: cfg.Adapter}
	if caps.Protocol != 0 {
		p.proto[cfg.Adapter] = caps.Protocol
	}
	p.mu.Unlock()
	return caps, nil
}

// adapterOf is ref's adapter ID ("" when it has none).
func (p *Pool) adapterOf(ref integrations.Ref) integrations.AdapterID {
	cfg, _ := p.reg.Config(ref)
	return cfg.Adapter
}

// toLocal maps a client path; "" when no mapping covers it.
func (p *Pool) toLocal(ctx context.Context, ref integrations.Ref, remote string) LocalPath {
	if remote == "" {
		return ""
	}
	if p.o.Paths != nil {
		if lp, err := p.o.Paths.ToLocal(Source(ref.Instance), remote); err == nil {
			return lp
		}
	}
	p.count(ctx, p.unmapped, attribute.String("instance", string(ref.Instance)))
	return ""
}

// toRemote maps a host path to ref's client; no mapping is ClassInvalid.
func (p *Pool) toRemote(ctx context.Context, ref integrations.Ref, local LocalPath) (string, error) {
	if local != "" && p.o.Paths != nil {
		if remote, err := p.o.Paths.ToRemote(Source(ref.Instance), local); err == nil {
			return remote, nil
		}
	}
	p.count(ctx, p.unmapped, attribute.String("instance", string(ref.Instance)))
	return "", &integrations.Error{Ref: ref, Op: "relocate", Class: integrations.ClassInvalid,
		Err: fmt.Errorf("%w: no mapping covers %s", integrations.ErrInvalid, string(local))}
}

// count adds one to c when metrics are on.
func (p *Pool) count(ctx context.Context, c metric.Int64Counter, a attribute.KeyValue) {
	if c != nil {
		c.Add(ctx, 1, metric.WithAttributes(a))
	}
}

// wrap makes err an *integrations.Error of ref and op: an adapter's own
// *integrations.Error passes through unchanged, anything else is classified
// by integrations.ClassOf against the caller's ctx.
func (p *Pool) wrap(ctx context.Context, ref integrations.Ref, op string, err error) error {
	if _, ok := errors.AsType[*integrations.Error](err); ok {
		return err
	}
	return &integrations.Error{Ref: ref, Op: op, Class: integrations.ClassOf(ctx, err), Err: err}
}
