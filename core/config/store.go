// file: core/config/store.go
// version: 1.0.5
// guid: 7ba3bd5f-b378-4795-8036-a466a5e1c750
// last-edited: 2026-11-10

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/secrets"
)

// Paths this file reads by name: config's own keys (05-config §1.7).
const (
	stateDirKey Path = "core.paths.state_dir"
)

// Health tokens the store raises on core.config (05-health).
const (
	healthBootRecordQuarantined = "config_boot_record_quarantined"
	healthOverridesQuarantined  = "config_overrides_quarantined"
	healthUnknownBootRecord     = "config_boot_record_unknown_keys"
)

// errClosed is every call after Close.
var errClosed = errors.New("config: store closed")

// Store is one App's configuration (C1): the registered sections, their
// layers, the published snapshots and the write path. One store per App; no
// package-level state.
type Store struct {
	o      Options
	prefix string
	log    *slog.Logger
	now    func() time.Time

	// mu serialises Register, Boot, Load, Attach, SetSecrets, Apply and Close
	// (05-config §1.6: Apply holds it from candidate build through publish).
	mu       sync.Mutex
	sections []*section // sorted by path
	envNames map[string]Path

	file fileSource     // the operator file (F3-02); unbuiltFile until then
	env  operatorSource // the environment (F3-02); nil until then

	sealed, booted, loaded, attached, closed bool

	op     map[Path]opValue
	ptVals map[Path]OperatorValue // the operator values of passthrough paths

	bootIO      *bootRecordIO
	boot        map[Path]json.RawMessage // every entry of the boot record, unknown ones included (current)
	bootVals    map[Path]json.RawMessage // usable boot values: known boot keys, canonical, not quarantined (current)
	bootAtLoad  map[Path]json.RawMessage // bootVals as Load saw them: what boot keys are effective with
	bootQuar    map[Path]error
	bootBadFile bool // the boot record was unparsable and set aside as .bad this boot
	bootUnknown []Path
	bootIgnored map[Path]string // usable boot values an operator layer locks -> lock reason
	bootLogged  map[Path]bool   // bootIgnored keys already logged this boot

	db         map[Path]Override        // every stored row (current)
	dbVals     map[Path]json.RawMessage // usable DB values (current)
	dbAtAttach map[Path]json.RawMessage // dbVals as Attach saw them: what restart keys are effective with
	quar       map[Path]error

	overrides    OverrideStore
	committer    Committer
	importTarget OverrideStore
	resolver     secrets.Resolver
	secretMeta   map[Path]*secrets.Meta

	gen    atomic.Uint64
	index  atomic.Pointer[keyIndex]
	health map[string]string

	// The subscriber goroutines (05-config §1.6): none before Load; Close
	// cancels and joins them all.
	subMu     sync.Mutex
	subsLive  bool
	subsDone  bool
	subs      []*subscription
	subCtx    context.Context
	subCancel context.CancelFunc
	wg        sync.WaitGroup

	met *storeMetrics
}

// keyIndex is the published, immutable key listing behind Lookup and Keys.
type keyIndex struct {
	byPath map[Path]KeyState
	sorted []Path
}

// validEnvPrefix reports whether p matches [A-Z][A-Z0-9]{1,15}.
func validEnvPrefix(p string) bool {
	if len(p) < 2 || len(p) > 16 {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// NewStore returns an empty store. It reads nothing and starts no goroutine.
func NewStore(o Options) (*Store, error) {
	if o.AppID == "" {
		return nil, errors.New("config: Options.AppID is required")
	}
	prefix := o.EnvPrefix
	if prefix == "" {
		prefix = strings.ToUpper(o.AppID)
	}
	if !validEnvPrefix(prefix) {
		return nil, fmt.Errorf("config: env prefix %q must match [A-Z][A-Z0-9]{1,15}", prefix)
	}
	s := &Store{
		o: o, prefix: prefix, log: o.Logger, now: o.Now,
		envNames: map[string]Path{}, op: map[Path]opValue{}, ptVals: map[Path]OperatorValue{},
		boot: map[Path]json.RawMessage{}, bootVals: map[Path]json.RawMessage{}, bootAtLoad: map[Path]json.RawMessage{},
		bootQuar: map[Path]error{}, bootIgnored: map[Path]string{}, bootLogged: map[Path]bool{}, db: map[Path]Override{}, dbVals: map[Path]json.RawMessage{},
		dbAtAttach: map[Path]json.RawMessage{}, quar: map[Path]error{}, secretMeta: map[Path]*secrets.Meta{},
		health: map[string]string{}, resolver: o.Secrets, met: newStoreMetrics(),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if o.FilePath != "" {
		s.file = unbuiltFile{path: o.FilePath}
	}
	s.subCtx, s.subCancel = context.WithCancel(context.Background())
	s.index.Store(&keyIndex{byPath: map[Path]KeyState{}})
	if o.Metrics != nil {
		if err := s.Instrument(o.Metrics); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// storeRegistry is the store's Registry.
type storeRegistry struct{ s *Store }

func (r storeRegistry) register(sp *sectionSpec) (*binding, error) { return r.s.register(sp) }

// Registry returns the registry the kernel registers sections through.
func (s *Store) Registry() Registry { return storeRegistry{s: s} }

// register validates and adds one section (05-config §1.3.1, §1.5).
func (s *Store) register(sp *sectionSpec) (*binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, errClosed
	case s.sealed:
		return nil, fmt.Errorf("%w: section %s", ErrSealed, sp.path)
	}
	sec, err := buildSection(sp)
	if err != nil {
		return nil, err
	}
	if err := s.checkOverlap(sec); err != nil {
		return nil, err
	}
	names := map[string]Path{}
	for _, eb := range sp.envBindings {
		if p, dup := s.envNames[eb.name]; dup {
			return nil, fmt.Errorf("%w: env name %s is bound to %s and %s", ErrDuplicate, eb.name, p, eb.path)
		}
		if p, dup := names[eb.name]; dup {
			return nil, fmt.Errorf("%w: env name %s is bound to %s and %s", ErrDuplicate, eb.name, p, eb.path)
		}
		names[eb.name] = eb.path
	}
	b := &binding{sec: sec, store: s}
	sec.bind = b
	empty := view{}
	built, err := s.build(sec, empty)
	if err != nil {
		return nil, fmt.Errorf("%w: section %s: its declared defaults fail: %w", ErrInvalid, sec.path, err)
	}
	maps.Copy(s.envNames, names)
	s.sections = append(s.sections, sec)
	slices.SortFunc(s.sections, func(a, b *section) int { return strings.Compare(string(a.path), string(b.path)) })
	s.install(sec, built, built, s.gen.Load(), true)
	s.reindex()
	paths := make([]Path, 0, len(s.sections))
	for _, x := range s.sections {
		paths = append(paths, x.path)
	}
	s.met.setSections(paths)
	return b, nil
}

// checkOverlap refuses a section whose path or keys collide with a
// registered one (05-config §1.3.1: a section path may extend another only at
// a segment the parent's type does not declare).
func (s *Store) checkOverlap(sec *section) error {
	for _, o := range s.sections {
		if o.path == sec.path {
			return fmt.Errorf("%w: section %s is registered twice", ErrDuplicate, sec.path)
		}
		for _, a := range sec.claims() {
			for _, b := range o.claims() {
				if overlaps(a, b) {
					return fmt.Errorf("%w: section %s's key %s overlaps section %s's key %s", ErrDuplicate, sec.path, a, o.path, b)
				}
			}
		}
		for _, b := range o.claims() {
			if under(sec.path, b) {
				return fmt.Errorf("%w: section %s lies under section %s's key %s", ErrDuplicate, sec.path, o.path, b)
			}
		}
		for _, a := range sec.claims() {
			if under(o.path, a) {
				return fmt.Errorf("%w: section %s's key %s holds section %s", ErrDuplicate, sec.path, a, o.path)
			}
		}
		// An entry list's parent is an ID namespace: "<parent>.<id>.<ref>"
		// is a secret key for any ID, so no other section may lie there.
		for _, pair := range [][2]*section{{sec, o}, {o, sec}} {
			for _, lk := range pair[0].entryLists {
				if under(pair[1].path, lk.entryParent()) {
					return fmt.Errorf("%w: section %s lies under %s, where section %s's list %s names its entries' secrets", ErrDuplicate, pair[1].path, lk.entryParent(), pair[0].path, lk.path(""))
				}
			}
		}
	}
	return nil
}

// effectiveView is what Get reads (05-config §1.4): boot keys as of Load,
// restart keys as of Attach, live keys current.
func (s *Store) effectiveView() view {
	return view{boot: s.bootAtLoad, live: s.dbVals, restart: s.dbAtAttach}
}

// pendingView is what Pending reads: every stored value, current.
func (s *Store) pendingView() view {
	return view{boot: s.bootVals, live: s.dbVals, restart: s.dbVals}
}

// Boot resolves the core sections registered so far from the operator layers
// and the boot record (06 P0). It reads no storage. A config file that sets
// core.paths.state_dir fails boot (05-config §1.7); the record is read through
// Options.BootRecordRoot called with the effective core.paths.state_dir,
// which the kernel registered with the directory ResolveStateDir chose as its
// default.
func (s *Store) Boot(ctx context.Context) (BootView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, errClosed
	case s.booted:
		return nil, errors.New("config: Boot called twice")
	}
	if err := s.bootLocked(); err != nil {
		return nil, err
	}
	return s.bootView(), nil
}

// bootLocked is Boot under s.mu.
func (s *Store) bootLocked() error {
	lr, err := s.readOperator(false)
	if err != nil {
		return err
	}
	if lr.fileSet[stateDirKey] {
		return fmt.Errorf("config: the operator file sets %s: it is a pre-boot key, read from the environment and flags only, because the state directory is resolved before any config file (05-config §1.7)", stateDirKey)
	}
	s.op, s.ptVals = lr.op, lr.passthrough
	stateDir := ""
	if c, ok := s.keyFor(stateDirKey); ok {
		var v string
		if err := json.Unmarshal(s.resolve(c, view{}).raw, &v); err != nil {
			return fmt.Errorf("config: %s: %w", stateDirKey, err)
		}
		stateDir = v
	}
	if s.o.BootRecordRoot != nil {
		root, err := s.o.BootRecordRoot(stateDir)
		if err != nil {
			return fmt.Errorf("config: open the boot record's directory %q: %w", stateDir, err)
		}
		if root == nil {
			return fmt.Errorf("config: BootRecordRoot returned no root for %q", stateDir)
		}
		s.bootIO = &bootRecordIO{root: root}
		if err := s.readBootRecord(); err != nil {
			return err
		}
	}
	s.classifyBoot()
	if err := s.settleAll(phaseBoot); err != nil {
		return err
	}
	s.booted = true
	return nil
}

// readBootRecord loads the record: absent is empty, unreadable fails closed,
// unparsable is renamed .bad and ignored (05-config §1.5).
func (s *Store) readBootRecord() error {
	b, err := s.bootIO.read()
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	rec, perr := parseBootRecord(b)
	if perr != nil {
		s.bootBadFile = true
		s.health[healthBootRecordQuarantined] = perr.Error()
		s.log.Warn("config: the boot record is unparsable; renamed to "+bootRecordBad+" and ignored",
			"file", bootRecordName, "err", perr)
		if err := s.bootIO.quarantine(); err != nil {
			return err
		}
		return nil
	}
	s.boot = rec.Values
	return nil
}

// classifyBoot splits the boot record into usable boot values and unknown
// entries (retained, reported like unknown override rows). A usable value of
// a key an operator layer sets is shadowed, as a stored row is (05-config
// §1.3.5): it is kept in the record, listed for AttachReport.Ignored and
// logged once per boot naming the key and the lock.
func (s *Store) classifyBoot() {
	s.bootVals = map[Path]json.RawMessage{}
	s.bootUnknown = nil
	s.bootIgnored = map[Path]string{}
	for _, p := range slices.Sorted(maps.Keys(s.boot)) {
		c, ok := s.keyFor(p)
		switch {
		case !ok || c.k.reload != ReloadBoot || c.k.protect == ProtectOperator:
			s.bootUnknown = append(s.bootUnknown, p)
			continue
		case s.bootQuar[p] != nil:
			continue
		}
		v, err := c.k.canonical(p, s.boot[p])
		if err != nil {
			s.bootQuar[p] = err
			continue
		}
		s.bootVals[p] = v
		if op, locked := s.op[p]; locked {
			s.bootIgnored[p] = op.by
			if !s.bootLogged[p] {
				s.bootLogged[p] = true
				s.log.Warn("config: a boot-record value is ignored because an operator layer sets the key",
					"key", p, "locked_by", op.by)
			}
		}
	}
	if len(s.bootUnknown) > 0 {
		s.health[healthUnknownBootRecord] = fmt.Sprintf("%d boot-record keys this binary does not declare", len(s.bootUnknown))
	} else {
		delete(s.health, healthUnknownBootRecord)
	}
	// A value that does not decode is ignored like a quarantined override
	// (05-config §1.5): kept in the record, never silently.
	if _, raised := s.health[healthBootRecordQuarantined]; !raised && len(s.bootQuar) > 0 {
		s.health[healthBootRecordQuarantined] = fmt.Sprintf("%d boot-record values ignored", len(s.bootQuar))
		s.log.Warn("config: boot-record values do not decode; they are kept and ignored", "keys", slices.Sorted(maps.Keys(s.bootQuar)))
	}
	s.bootAtLoad = maps.Clone(s.bootVals)
}

// phase names which stored layer a settle may quarantine.
type phase int

const (
	phaseBoot phase = iota + 1
	phaseLoad
	phaseAttach
)

// settleAll builds every registered section with the stored values in play,
// quarantines a section's stored values when it fails with them and passes
// without them, fails on a section that fails without them (an operator or
// declaration error), and publishes the result.
func (s *Store) settleAll(ph phase) error {
	for _, sec := range s.sections {
		if err := s.settle(sec, ph); err != nil {
			return err
		}
	}
	s.bootAtLoad = maps.Clone(s.bootVals)
	if ph == phaseAttach {
		s.dbAtAttach = maps.Clone(s.dbVals)
	}
	for _, sec := range s.sections {
		eff, err := s.build(sec, s.effectiveView())
		if err != nil {
			return fmt.Errorf("config: section %s: %w", sec.path, err)
		}
		pend, err := s.build(sec, s.pendingView())
		if err != nil {
			return fmt.Errorf("config: section %s: %w", sec.path, err)
		}
		s.install(sec, eff, pend, s.gen.Load(), false)
	}
	s.reindex()
	return nil
}

// settle checks one section; see settleAll.
func (s *Store) settle(sec *section, ph phase) error {
	try := func(v view) error {
		if _, err := s.build(sec, v); err != nil {
			return err
		}
		return nil
	}
	// Attach is the moment restart keys take their stored value, so the
	// effective view then reads them as stored.
	v := view{boot: s.bootVals, live: s.dbVals, restart: s.dbVals}
	err := try(v)
	if err == nil {
		return nil
	}
	stripped := v
	var held []Path
	switch ph {
	case phaseBoot, phaseLoad:
		stripped.boot = withoutSection(v.boot, sec, s)
		held = sectionPaths(v.boot, sec, s)
	case phaseAttach:
		stripped.live = withoutSection(v.live, sec, s)
		stripped.restart = stripped.live
		held = sectionPaths(v.live, sec, s)
	}
	if len(held) == 0 {
		return fmt.Errorf("config: section %s (%s): %w", sec.path, s.operatorSources(sec), invalid(err))
	}
	if err2 := try(stripped); err2 != nil {
		return fmt.Errorf("config: section %s (%s): %w", sec.path, s.operatorSources(sec), invalid(err2))
	}
	for _, p := range held {
		switch ph {
		case phaseAttach:
			s.quar[p] = err
			delete(s.dbVals, p)
		default:
			s.bootQuar[p] = err
			delete(s.bootVals, p)
		}
	}
	token := healthOverridesQuarantined
	if ph != phaseAttach {
		token = healthBootRecordQuarantined
	}
	s.health[token] = fmt.Sprintf("section %s: %v", sec.path, err)
	s.log.Warn("config: stored values of a section fail validation; they are kept and ignored",
		"section", sec.path, "keys", held, "err", err)
	return nil
}

// sectionPaths lists m's paths owned by sec, sorted.
func sectionPaths(m map[Path]json.RawMessage, sec *section, s *Store) []Path {
	var out []Path
	for p := range m {
		if s.sectionOf(p) == sec {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// Load seals the registry and builds every section from defaults, operator
// layers and the boot record (06 P3). A section whose operator values fail
// decode or Validate fails boot; a flag naming no registered key fails boot.
func (s *Store) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return errClosed
	case s.loaded:
		return errors.New("config: Load called twice")
	}
	if !s.booted {
		if err := s.bootLocked(); err != nil {
			return err
		}
	}
	lr, err := s.readOperator(true)
	if err != nil {
		return err
	}
	if lr.fileSet[stateDirKey] {
		return fmt.Errorf("config: the operator file sets %s: it is a pre-boot key (05-config §1.7)", stateDirKey)
	}
	s.op, s.ptVals = lr.op, lr.passthrough
	s.sealed = true
	s.classifyBoot()
	if err := s.settleAll(phaseLoad); err != nil {
		s.sealed = false
		return err
	}
	s.refreshSecrets(ctx, nil)
	s.reindex()
	s.loaded = true
	s.startSubs()
	return nil
}

// SetSecrets binds the resolver every secrets.Ref of a published snapshot
// resolves through (06 P4d step 1, before Attach) and republishes.
func (s *Store) SetSecrets(r secrets.Resolver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolver = r
	if s.closed {
		return
	}
	for _, sec := range s.sections {
		eff, err := s.build(sec, s.effectiveView())
		if err != nil {
			s.log.Error("config: rebuilding a section after SetSecrets failed", "section", sec.path, "err", err)
			continue
		}
		pend, err := s.build(sec, s.pendingView())
		if err != nil {
			s.log.Error("config: rebuilding a section after SetSecrets failed", "section", sec.path, "err", err)
			continue
		}
		s.install(sec, eff, pend, s.gen.Load(), true)
	}
	s.reindex()
}

// Attach reads the DB layer through o, once per boot, writing nothing (06
// P4d step 1; C13). Rows of keys an operator layer supplies are ignored and
// kept; a section that fails with its rows but passes without them has every
// row quarantined and boot continues. c is the write path Apply uses.
func (s *Store) Attach(ctx context.Context, o OverrideStore, c Committer) (AttachReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := AttachReport{Quarantined: map[Path]error{}, Ignored: maps.Clone(s.bootIgnored), FailClosed: map[Path]string{}}
	switch {
	case s.closed:
		return rep, errClosed
	case !s.loaded:
		return rep, errors.New("config: Attach before Load")
	case s.attached:
		return rep, errors.New("config: Attach runs once per boot")
	case o == nil || c == nil:
		return rep, errors.New("config: Attach needs the override store and the committer")
	}
	rows := map[Path]Override{}
	for ov, err := range o.Load(ctx) {
		if err != nil {
			return AttachReport{}, fmt.Errorf("config: read the stored overrides: %w", err)
		}
		rows[ov.Path] = ov
	}
	prevQuar := maps.Clone(s.quar)
	quar := map[Path]error{}
	vals := map[Path]json.RawMessage{}
	bad := map[*section]Path{} // a section with an undecodable row: the first such row
	for _, p := range slices.Sorted(maps.Keys(rows)) {
		ck, ok := s.keyFor(p)
		switch {
		case !ok || s.passthrough(p):
			rep.Unknown = append(rep.Unknown, p)
			continue
		case ck.k.secret:
			quar[p] = fmt.Errorf("%s is a secret key: its value lives in the secrets store, never in c/config/", p)
			continue
		case ck.k.protect == ProtectOperator:
			quar[p] = fmt.Errorf("%s is an operator-only key: never read from the DB layer", p)
			continue
		case ck.k.reload == ReloadBoot:
			quar[p] = fmt.Errorf("%s is a boot key: its stored value lives in the boot record", p)
			continue
		}
		if op, locked := s.op[p]; locked {
			rep.Ignored[p] = op.by
			s.log.Warn("config: a stored value is ignored because an operator layer sets the key",
				"key", p, "locked_by", op.by)
			continue
		}
		v, err := ck.k.canonical(p, rows[p].Value)
		if err != nil {
			quar[p] = err
			if _, seen := bad[ck.k.sec]; !seen {
				bad[ck.k.sec] = p
			}
			continue
		}
		vals[p] = v
	}
	// A row that does not decode is the section failing with its overrides:
	// every row of that section is quarantined (05-config §1.4 step 6). It is
	// never handed to the build, where a missing value would read as JSON
	// null and decode to the zero value.
	for _, sec := range s.sections {
		first, ok := bad[sec]
		if !ok {
			continue
		}
		for _, p := range sectionPaths(vals, sec, s) {
			quar[p] = fmt.Errorf("quarantined with its section %s, whose row %s does not decode: %w", sec.path, first, quar[first])
			delete(vals, p)
		}
		s.log.Warn("config: a stored row does not decode; its section's rows are kept and ignored",
			"section", sec.path, "key", first, "err", quar[first])
	}
	if len(quar) > 0 {
		s.health[healthOverridesQuarantined] = fmt.Sprintf("%d stored overrides ignored", len(quar))
	}
	s.db, s.dbVals, s.quar = rows, vals, quar
	if err := s.settleAll(phaseAttach); err != nil {
		s.db, s.dbVals, s.quar = map[Path]Override{}, map[Path]json.RawMessage{}, prevQuar
		delete(s.health, healthOverridesQuarantined)
		return AttachReport{}, err
	}
	maps.Copy(rep.Quarantined, s.quar)
	rep.Applied = len(s.dbVals)
	s.overrides, s.committer, s.attached = o, c, true
	s.refreshSecrets(ctx, nil)
	s.reindex()
	s.met.setQuarantined(len(s.quar))
	return rep, nil
}

// install publishes a section's effective and pending builds. A new effective
// snapshot replaces the old one only when its JSON changed (or force: the
// resolver changed); Pending is the effective snapshot itself when equal.
func (s *Store) install(sec *section, eff, pend *built, gen uint64, force bool) (prevEff, nextEff, prevPend, nextPend *snapshot) {
	b := sec.bind
	prevEff, prevPend = b.cur.Load(), b.pend.Load()
	nextEff = prevEff
	if force || prevEff == nil || string(prevEff.raw) != string(eff.raw) {
		g := gen
		if prevEff != nil && string(prevEff.raw) == string(eff.raw) {
			g = prevEff.gen
		}
		nextEff = &snapshot{val: eff.val, raw: eff.raw, gen: g}
		b.cur.Store(nextEff)
	}
	nextPend = nextEff
	if string(pend.raw) != string(eff.raw) {
		nextPend = prevPend
		if force || prevPend == nil || string(prevPend.raw) != string(pend.raw) {
			nextPend = &snapshot{val: pend.val, raw: pend.raw, gen: gen}
		}
	}
	b.pend.Store(nextPend)
	sec.eff, sec.pend = eff, pend
	return prevEff, nextEff, prevPend, nextPend
}

// reindex republishes the key index from every section's last builds.
func (s *Store) reindex() {
	idx := &keyIndex{byPath: map[Path]KeyState{}}
	pendingKeys := 0
	for _, sec := range s.sections {
		if sec.eff == nil {
			continue
		}
		keys := map[Path]ckey{}
		for _, c := range sec.eff.keys {
			keys[c.p] = c
		}
		for _, c := range sec.pend.keys {
			keys[c.p] = c
		}
		for _, p := range slices.Sorted(maps.Keys(keys)) {
			c := keys[p]
			ev, ok := sec.eff.vals[p]
			if !ok {
				ev = keyVal{raw: c.def(), src: SourceDefault}
			}
			pv, ok := sec.pend.vals[p]
			if !ok {
				pv = ev
			}
			ks := KeyState{
				Path: p, Section: sec.path, Schema: c.k.schemaRaw, Effective: ev.raw, Source: ev.src,
				Reload: c.k.reload, Protection: c.k.protect, FailClosed: c.k.failOrder != nil,
				Pending: string(ev.raw) != string(pv.raw),
			}
			if ev.src.operator() {
				ks.LockedBy = ev.by
			}
			if e := s.quar[p]; e != nil {
				ks.Quarantined = e
			} else if e := s.bootQuar[p]; e != nil {
				ks.Quarantined = e
			}
			if c.k.secret {
				ks.Effective = nil
				// The build ran before refreshSecrets read the Meta, so the
				// secret's source is read now, not from the build.
				if !ks.Source.operator() {
					ks.Source = s.secretSource(p)
				}
				m := s.secretMeta[p]
				if m == nil {
					m = &secrets.Meta{Name: secrets.Name(p)}
				}
				ks.Secret = m
			}
			if ks.Pending {
				pendingKeys++
			}
			idx.byPath[p] = ks
			idx.sorted = append(idx.sorted, p)
		}
	}
	slices.Sort(idx.sorted)
	s.index.Store(idx)
	s.met.setRestartPending(pendingKeys)
}

// liveSecrets is every secret key the sections' last builds hold: the fixed
// ones and each list entry's.
func (s *Store) liveSecrets() map[Path]bool {
	live := map[Path]bool{}
	for _, sec := range s.sections {
		for _, b := range []*built{sec.eff, sec.pend} {
			if b == nil {
				continue
			}
			for _, c := range b.keys {
				if c.k.secret {
					live[c.p] = true
				}
			}
		}
	}
	return live
}

// refreshSecrets re-reads the Meta of the secret keys named in only (every
// secret key when nil) through the resolver. A named key no build holds any
// more (a removed list entry's) loses its Meta.
func (s *Store) refreshSecrets(ctx context.Context, only []Path) {
	live := s.liveSecrets()
	for p := range s.secretMeta {
		if !live[p] && (only == nil || slices.Contains(only, p)) {
			delete(s.secretMeta, p)
		}
	}
	for _, p := range slices.Sorted(maps.Keys(live)) {
		if only != nil && !slices.Contains(only, p) {
			continue
		}
		m := secrets.Meta{Name: secrets.Name(p)}
		if s.resolver != nil {
			got, err := s.resolver.Meta(ctx, secrets.Name(p))
			if err != nil {
				got = secrets.Meta{Name: secrets.Name(p), Err: err}
			}
			m = got
		}
		s.secretMeta[p] = &m
	}
}

// cloneState returns a copy of ks whose slices and Meta are the caller's.
func cloneState(ks KeyState) KeyState {
	ks.Schema = slices.Clone(ks.Schema)
	ks.Effective = slices.Clone(ks.Effective)
	if ks.Secret != nil {
		m := *ks.Secret
		ks.Secret = &m
	}
	return ks
}

// Lookup returns p's state. Lock-free: one atomic load of the published
// index.
func (s *Store) Lookup(p Path) (KeyState, bool) {
	ks, ok := s.index.Load().byPath[p]
	if !ok {
		return KeyState{}, false
	}
	return cloneState(ks), true
}

// Keys iterates every key, sorted by path; secret values are never included.
func (s *Store) Keys() iter.Seq2[Path, KeyState] {
	idx := s.index.Load()
	return func(yield func(Path, KeyState) bool) {
		for _, p := range idx.sorted {
			if !yield(p, cloneState(idx.byPath[p])) {
				return
			}
		}
	}
}

// RetiredSet lists the retired names set at this boot. Retired names are
// F3-02's: until then it is nil.
func (s *Store) RetiredSet() []RetiredUse { return nil }

// PassthroughValues iterates the operator-layer values of passthrough paths,
// raw, sorted by path; the kernel hands them to integrations (C11).
func (s *Store) PassthroughValues() iter.Seq2[Path, OperatorValue] {
	s.mu.Lock()
	pt := maps.Clone(s.ptVals)
	s.mu.Unlock()
	return func(yield func(Path, OperatorValue) bool) {
		for _, p := range slices.Sorted(maps.Keys(pt)) {
			v := pt[p]
			v.Value = slices.Clone(v.Value)
			if !yield(p, v) {
				return
			}
		}
	}
}

// Generation is the number of successful Applies.
func (s *Store) Generation() uint64 { return s.gen.Load() }

// Close cancels the subscribers' context and joins every subscriber
// goroutine, waiting until ctx ends (the kernel bounds it with
// core.config.subscriber_drain_timeout), then closes the boot record's root.
func (s *Store) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	io := s.bootIO
	s.mu.Unlock()

	s.subMu.Lock()
	s.subsLive, s.subsDone = false, true
	s.subMu.Unlock()
	s.subCancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = fmt.Errorf("config: subscribers still running at the drain deadline: %w", ctx.Err())
	}
	if io != nil {
		if cerr := io.root.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("config: close the boot record's directory: %w", cerr))
		}
	}
	return err
}

// startSubs starts every subscription registered before Load.
func (s *Store) startSubs() {
	s.subMu.Lock()
	if s.subsDone {
		s.subMu.Unlock()
		return
	}
	s.subsLive = true
	subs := slices.Clone(s.subs)
	s.subMu.Unlock()
	for _, sub := range subs {
		s.startSub(sub)
	}
}

// startSub starts sub's goroutine once the store is loaded; before that it is
// only recorded.
func (s *Store) startSub(sub *subscription) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if !slices.Contains(s.subs, sub) {
		s.subs = append(s.subs, sub)
	}
	if !s.subsLive || s.subsDone {
		return
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.started || sub.stopped() {
		return
	}
	sub.started = true
	s.wg.Add(1)
	go sub.run(s.subCtx, s.wg.Done)
}

// subscriberPanicked records a recovered OnChange panic.
func (s *Store) subscriberPanicked(sec Path, r any) {
	s.log.Error("config: an OnChange subscriber panicked; recovered", "section", sec, "panic", fmt.Sprint(r))
	s.met.subscriberPanic(sec)
}

// syncQuarantineHealth makes the two quarantine health tokens match the
// quarantine state after an Apply, as Attach and Load set them: each is raised
// while its layer holds a quarantined value and lowered once none is left
// (05-config §1.4 step 6, §1.5). Apply only clears quarantines today, so in
// practice it lowers them; raising is here so the tokens follow the state
// whichever way it moves. A boot record set aside whole as .bad keeps
// config_boot_record_quarantined raised for the rest of the boot: no Apply
// undoes that, and the operator still has the .bad file to look at.
func (s *Store) syncQuarantineHealth() {
	set := func(token string, on bool, msg string) {
		_, raised := s.health[token]
		switch {
		case !on:
			delete(s.health, token)
		case !raised:
			s.health[token] = msg
		}
	}
	set(healthOverridesQuarantined, len(s.quar) > 0, fmt.Sprintf("%d stored overrides ignored", len(s.quar)))
	bootMsg := fmt.Sprintf("%d boot-record values ignored", len(s.bootQuar))
	if s.bootBadFile {
		bootMsg = "the boot record was unparsable and set aside as " + bootRecordBad
	}
	set(healthBootRecordQuarantined, len(s.bootQuar) > 0 || s.bootBadFile, bootMsg)
}

// HealthTokens lists the store's raised health tokens (name coined by
// F3-01: 05-config names config_boot_record_quarantined and
// config_overrides_quarantined, and the kernel reports them on core.config).
func (s *Store) HealthTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.health))
}

// SetImportTarget gives Importer the override store its rows are staged
// through (name coined by F3-01): the cutover step M2 runs in 06 P4b, before
// Attach, so the native store is handed over as soon as the kernel builds it.
func (s *Store) SetImportTarget(o OverrideStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.importTarget = o
}

// Operator returns p's operator-layer value (flag > env > file), before or
// after Boot, registered or not (name coined by F3-01: the kernel's step 0
// reads core.storage.path from every pre-boot layer, the file included,
// before Boot; 05-config §1.7, OPS5-13). A registered key's value is
// canonical; an unregistered path's flag text is a JSON string.
func (s *Store) Operator(p Path) (OperatorValue, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.query()
	q.want = map[Path]bool{p: true}
	var out OperatorValue
	found := false
	ck, known := s.keyFor(p)
	for _, l := range []struct {
		src Source
		os  operatorSource
	}{{SourceFile, s.file}, {SourceEnv, s.env}} {
		if l.os == nil {
			continue
		}
		vals, err := l.os.read(q)
		if err != nil {
			return OperatorValue{}, false, err
		}
		for _, v := range vals {
			if v.path != p {
				continue
			}
			raw := v.raw
			if known {
				c, err := ck.k.canonical(p, raw)
				if err != nil {
					return OperatorValue{}, false, fmt.Errorf("config: %s: %w", v.by, invalid(err))
				}
				raw = c
			}
			out, found = OperatorValue{Value: raw, Src: l.src, By: v.by}, true
		}
	}
	if text, ok := s.o.Flags[p]; ok {
		var k *keyInfo
		if known {
			k = ck.k
		}
		raw, err := flagJSON(k, p, text)
		if err != nil {
			return OperatorValue{}, false, fmt.Errorf("config: %s: %w", flagBy(p), invalid(err))
		}
		if known {
			if raw, err = ck.k.canonical(p, raw); err != nil {
				return OperatorValue{}, false, fmt.Errorf("config: %s: %w", flagBy(p), invalid(err))
			}
		}
		out, found = OperatorValue{Value: raw, Src: SourceFlag, By: flagBy(p)}, true
	}
	return out, found, nil
}

// dropForFile reports the paths whose file values are secret: secret keys,
// passthrough paths (integration-bound values may be credentials) and lists
// whose entries hold secrets (fail closed: an operator may paste a secret
// into the list in the file, which then refuses to boot but is still on disk
// to be archived).
func (s *Store) dropForFile(p Path) bool {
	if s.passthrough(p) {
		return true
	}
	c, ok := s.keyFor(p)
	return ok && (c.k.secret || len(c.k.entrySecrets) > 0)
}

// ConfigFileHasSecrets reports whether the operator file supplies a value for
// any secret key or any passthrough path; 05-backup then archives the file as
// class Secret (DATA2-17). An unreadable file counts as holding secrets.
func (s *Store) ConfigFileHasSecrets() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return false
	}
	has, err := s.file.holds(s.query(), s.dropForFile)
	if err != nil {
		s.log.Warn("config: cannot tell whether the operator file holds secrets; treating it as holding them", "err", err)
		return true
	}
	return has
}

// RedactedConfigFile renders the operator file without its secret and
// passthrough values, for unencrypted archives. nil when there is no file.
func (s *Store) RedactedConfigFile() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil, nil
	}
	return s.file.redacted(s.query(), s.dropForFile)
}

// bootView is Boot's result: the boot and operator-only keys of the core
// sections, frozen at Boot.
type bootView struct {
	vals map[Path]keyVal
	maps map[Path]json.RawMessage
	srcs map[Path]Source
}

// bootView freezes the core sections' boot and operator-only keys. A map
// section's entry key is listed only when a layer supplies it (its default
// is the kernel's: each manifest's DefaultEnabled).
func (s *Store) bootView() BootView {
	bv := &bootView{vals: map[Path]keyVal{}, maps: map[Path]json.RawMessage{}, srcs: map[Path]Source{}}
	for _, sec := range s.sections {
		if !sec.core() || sec.eff == nil {
			continue
		}
		explicit := map[string]map[string]json.RawMessage{}
		for _, c := range sec.eff.keys {
			if c.k.reload != ReloadBoot && c.k.protect != ProtectOperator {
				continue
			}
			kv := sec.eff.vals[c.p]
			if sec.isMap && kv.src == SourceDefault {
				continue
			}
			bv.vals[c.p] = kv
			if sec.isMap {
				if explicit[c.id] == nil {
					explicit[c.id] = map[string]json.RawMessage{}
				}
				explicit[c.id][c.k.relName()] = kv.raw
			}
		}
		if sec.isMap {
			b, err := json.Marshal(explicit)
			if err == nil {
				bv.maps[sec.path] = b
			}
		}
	}
	return bv
}

// Bool returns a boolean boot key.
func (b *bootView) Bool(p Path) (bool, Source, bool) {
	kv, ok := b.vals[p]
	if !ok {
		return false, 0, false
	}
	var v bool
	if err := json.Unmarshal(kv.raw, &v); err != nil {
		return false, 0, false
	}
	return v, kv.src, true
}

// Raw returns a boot key's JSON, or a map section's explicit entries.
func (b *bootView) Raw(p Path) (json.RawMessage, Source, bool) {
	if m, ok := b.maps[p]; ok {
		return slices.Clone(m), 0, true
	}
	kv, ok := b.vals[p]
	if !ok {
		return nil, 0, false
	}
	return slices.Clone(kv.raw), kv.src, true
}
