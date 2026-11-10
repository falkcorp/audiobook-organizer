// file: core/storage/migrate.go
// version: 1.1.0
// guid: cbe1923b-9a7c-4207-87b3-a13fb6e46ea1
// last-edited: 2026-11-10

package storage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// highestVersion is an owner's highest registered step version.
func highestVersion(s Spec) int {
	top := 0
	for _, l := range s.Legacy {
		top = max(top, l.Version)
	}
	for _, m := range s.Migrations {
		top = max(top, m.Version)
	}
	return top
}

// specProblems is §5S.3.5's registration rules for one owner: versions 1..n
// contiguous (legacy steps first), a non-zero Compat, Breaking with
// RaisesLevel (and Additive without), Seed only with Additive, and members
// the owner can use.
func specProblems(o *ownerInfo) []string {
	var probs []string
	s := o.spec
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf("%s: ", o.owner)+fmt.Sprintf(f, a...)) }
	hasClaims := len(s.Claims) > 0
	seen := map[int]bool{}
	for i, l := range s.Legacy {
		if l.Version != i+1 {
			add("legacy step %d %q: legacy versions are 1..LegacyCap in order", l.Version, l.Name)
		}
		if l.Name == "" || l.Up == nil {
			add("legacy step %d needs a Name and an Up", l.Version)
		}
		seen[l.Version] = true
	}
	next := len(s.Legacy) + 1
	for i, m := range s.Migrations {
		if m.Version != next+i {
			add("step %d %q: versions are contiguous; want %d", m.Version, m.Name, next+i)
		}
		if seen[m.Version] {
			add("step %d declared twice", m.Version)
		}
		seen[m.Version] = true
		if m.Name == "" || m.Up == nil {
			add("step %d needs a Name and an Up", m.Version)
		}
		switch m.Compat {
		case Additive:
			if m.RaisesLevel != 0 {
				add("step %d %q: an Additive step raises no level; a level step is Breaking", m.Version, m.Name)
			}
		case Breaking:
			if m.RaisesLevel <= 0 {
				add("step %d %q: a Breaking step requires RaisesLevel", m.Version, m.Name)
			} else if m.RaisesLevel > s.Level {
				add("step %d %q raises the level to %d, above the build's Level %d", m.Version, m.Name, m.RaisesLevel, s.Level)
			}
		default:
			add("step %d %q: Compat is zero", m.Version, m.Name)
		}
		if m.Seed && m.Compat != Additive {
			add("step %d %q: Seed is allowed only with Additive", m.Version, m.Name)
		}
		if !s.Keyspace && !hasClaims {
			add("step %d %q: an owner with neither a keyspace nor claims has nothing to migrate", m.Version, m.Name)
		}
	}
	if s.Level < 0 {
		add("Level %d is negative", s.Level)
	}
	if s.MinVersionToServe < 0 || s.MinVersionToServe > highestVersion(s) {
		add("MinVersionToServe %d is outside 0..%d", s.MinVersionToServe, highestVersion(s))
	}
	if len(s.Legacy) > 0 && s.VersionSource == nil {
		add("legacy steps keep their bookkeeping through a VersionSource, and none is declared")
	}
	if (len(s.Legacy) > 0 || s.VersionSource != nil || s.LegacyGuard != nil) && !hasClaims {
		add("Legacy, VersionSource and LegacyGuard need legacy claims")
	}
	if g := s.LegacyGuard; g != nil {
		if g.StampKey == "" || g.MarkerKey == "" || g.SidecarSuffix == "" || g.Stage == nil || g.LegacyMaxFormat < 1 {
			add("LegacyGuard needs StampKey, MarkerKey, SidecarSuffix, Stage and LegacyMaxFormat >= 1")
		}
	}
	if f := s.UpgradeFloor; f != nil && f.Owner != o.owner {
		add("UpgradeFloor.Owner %q is not the declaring owner", f.Owner)
	}
	return probs
}

// stepNode is one migration step of the DAG: a books legacy step, a
// versioned step or a data migration (DataStep).
type stepNode struct {
	ref    StepRef
	owner  *ownerInfo
	legacy *LegacyMigration
	mig    *Migration
	data   *DataStep
	deps   []StepRef
}

func (n *stepNode) name() string {
	switch {
	case n.legacy != nil:
		return n.legacy.Name
	case n.data != nil:
		return n.data.ID
	}
	return n.mig.Name
}

// order is the node's place among its owner's steps for the tie-break:
// versioned and legacy steps by version, then data steps as declared.
func (n *stepNode) order() int {
	if n.data != nil {
		return math.MaxInt32 + n.data.order
	}
	return n.ref.Version
}

// stepGraph builds every registered owner's step nodes with their edges: the
// owner's previous version, every After, the implicit edge from every core
// owner's steps to the highest legacy step (§5S.3.5 "Order"), and a data
// step's edges (DataStep).
func stepGraph(owners map[Owner]*ownerInfo) map[string]*stepNode {
	nodes := map[string]*stepNode{}
	var legacyTops []StepRef
	for _, o := range sortedInfos(owners) {
		for i := range o.spec.Legacy {
			l := &o.spec.Legacy[i]
			nodes[StepRef{Owner: o.owner, Version: l.Version}.String()] = &stepNode{ref: StepRef{Owner: o.owner, Version: l.Version}, owner: o, legacy: l}
		}
		if n := len(o.spec.Legacy); n > 0 {
			legacyTops = append(legacyTops, StepRef{Owner: o.owner, Version: o.spec.Legacy[n-1].Version})
		}
		for i := range o.spec.Migrations {
			m := &o.spec.Migrations[i]
			nodes[StepRef{Owner: o.owner, Version: m.Version}.String()] = &stepNode{ref: StepRef{Owner: o.owner, Version: m.Version}, owner: o, mig: m}
		}
		for _, d := range o.data {
			nodes[d.ref().String()] = &stepNode{ref: d.ref(), owner: o, data: d}
		}
	}
	for _, n := range nodes {
		if n.data != nil {
			n.deps = append(n.deps, n.data.After...)
			// After the owner's versioned steps below Barrier (all when 0):
			// the highest of them, since an owner's versions chain.
			below := highestVersion(n.owner.spec)
			if b := n.data.Barrier; b > 0 {
				below = min(below, b-1)
			}
			if below > 0 {
				n.deps = append(n.deps, StepRef{Owner: n.ref.Owner, Version: below})
			}
			if n.owner.core {
				n.deps = append(n.deps, legacyTops...)
			}
			// The Barrier version, and so every later one, after this step.
			if b := n.data.Barrier; b > 0 {
				if bn, ok := nodes[StepRef{Owner: n.ref.Owner, Version: b}.String()]; ok {
					bn.deps = append(bn.deps, n.ref)
				}
			}
			continue
		}
		if n.ref.Version > 1 {
			n.deps = append(n.deps, StepRef{Owner: n.ref.Owner, Version: n.ref.Version - 1})
		}
		if n.mig != nil {
			n.deps = append(n.deps, n.mig.After...)
		}
		if n.owner.core {
			n.deps = append(n.deps, legacyTops...)
		}
	}
	return nodes
}

// checkDAGShape is the registration half of §5S.3.5 "Order": a core owner
// with steps must be in the fixed core order, an After naming a registered
// owner must name a registered step, and the full graph has no cycle.
func checkDAGShape(owners map[Owner]*ownerInfo) error {
	var probs []string
	for _, o := range sortedInfos(owners) {
		if o.core && (len(o.spec.Migrations) > 0 || len(o.spec.Legacy) > 0 || len(o.data) > 0) && !slices.Contains(coreOrder(), coreSub(o.owner)) {
			probs = append(probs, fmt.Sprintf("%s declares steps and is not in the kernel's core order (unordered owner)", o.owner))
		}
	}
	nodes := stepGraph(owners)
	for _, n := range sortedNodes(nodes) {
		for _, d := range n.deps {
			if _, registered := owners[d.Owner]; !registered {
				continue // an absent owner defers the step at boot, unless the edge is applied
			}
			if _, ok := nodes[d.String()]; !ok {
				probs = append(probs, fmt.Sprintf("%s runs after %s, an unknown step of a registered owner", n.ref, d))
			}
		}
	}
	// Cycle detection (three-colour DFS).
	colour := map[string]int{}
	var stack []string
	var visit func(k string) bool
	visit = func(k string) bool {
		switch colour[k] {
		case 1:
			i := slices.Index(stack, k)
			probs = append(probs, "cycle: "+strings.Join(append(slices.Clone(stack[i:]), k), " -> "))
			return false
		case 2:
			return true
		}
		colour[k] = 1
		stack = append(stack, k)
		for _, d := range nodes[k].deps {
			if _, ok := nodes[d.String()]; ok && !visit(d.String()) {
				return false
			}
		}
		stack = stack[:len(stack)-1]
		colour[k] = 2
		return true
	}
	for _, n := range sortedNodes(nodes) {
		if colour[n.ref.String()] == 0 && !visit(n.ref.String()) {
			break
		}
	}
	if len(probs) > 0 {
		return fmt.Errorf("%w: %s", ErrMigrationCycle, strings.Join(probs, "; "))
	}
	return nil
}

func sortedNodes(nodes map[string]*stepNode) []*stepNode {
	out := make([]*stepNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n)
	}
	slices.SortFunc(out, compareNodes)
	return out
}

// ownerRank orders owners for the DAG's tie-break: core subsystems in the
// kernel's fixed order, then modules by ID.
func ownerRank(o *ownerInfo) (int, string) {
	if o.core {
		return slices.Index(coreOrder(), coreSub(o.owner)), ""
	}
	return len(coreOrder()), string(o.owner)
}

func compareNodes(a, b *stepNode) int {
	ra, sa := ownerRank(a.owner)
	rb, sb := ownerRank(b.owner)
	if ra != rb {
		return ra - rb
	}
	if c := strings.Compare(sa, sb); c != 0 {
		return c
	}
	return cmp.Compare(a.order(), b.order())
}

// planDAG computes the boot's runnable steps in order and the deferred ones.
// A versioned step is pending when its version is above the owner's applied
// version, a data step while its record is not done. A pending step of a
// disabled owner is deferred (a cutover data step is not, 06 A5), and so is
// every pending step with an unapplied dependency that is deferred or whose
// owner is absent; an edge to an applied step is satisfied whoever owns it.
func (db *DB) planDAG(ctx context.Context) error {
	st := db.boot
	nodes := stepGraph(db.owners)
	pending := map[string]*stepNode{}
	for k, n := range nodes {
		if n.data != nil {
			done, err := db.stepDone(ctx, n.ref)
			if err != nil {
				return err
			}
			if !done {
				pending[k] = n
			}
			continue
		}
		if n.ref.Version > st.versions[n.ref.Owner] {
			pending[k] = n
		}
	}
	deferred := map[string]bool{}
	satisfied := func(d StepRef) (bool, error) {
		if _, ok := pending[d.String()]; ok {
			return false, nil
		}
		if _, ok := nodes[d.String()]; ok {
			return true, nil // registered and applied
		}
		return db.stepDone(ctx, d) // an absent owner's step, or a data migration
	}
	for changed := true; changed; {
		changed = false
		for _, n := range sortedNodes(pending) {
			k := n.ref.String()
			if deferred[k] {
				continue
			}
			def := !n.owner.enabled && (n.data == nil || !n.data.Cutover)
			for _, d := range n.deps {
				if def {
					break
				}
				ok, err := satisfied(d)
				if err != nil {
					return err
				}
				if ok {
					continue
				}
				if _, isPending := pending[d.String()]; !isPending || deferred[d.String()] {
					def = true
				}
			}
			if def {
				deferred[k], changed = true, true
			}
		}
	}
	// Kahn's algorithm over the runnable steps, the smallest ready step first.
	ready := func(n *stepNode, done map[string]bool) bool {
		for _, d := range n.deps {
			if _, isPending := pending[d.String()]; isPending && !done[d.String()] {
				return false
			}
		}
		return true
	}
	done := map[string]bool{}
	st.plan, st.deferred = nil, nil
	var left []*stepNode
	for _, n := range sortedNodes(pending) {
		if deferred[n.ref.String()] {
			st.deferred = append(st.deferred, n)
		} else {
			left = append(left, n)
		}
	}
	for len(left) > 0 {
		i := slices.IndexFunc(left, func(n *stepNode) bool { return ready(n, done) })
		if i < 0 {
			return fmt.Errorf("%w: no runnable step among %d pending", ErrMigrationCycle, len(left))
		}
		n := left[i]
		left = slices.Delete(left, i, i+1)
		done[n.ref.String()] = true
		st.plan = append(st.plan, n)
	}
	for _, n := range st.deferred {
		db.log.Warn("storage: migration step deferred behind a disabled or absent owner", "step", n.ref.String(), "name", n.name())
	}
	return nil
}

// Plan is the boot's runnable steps in DAG order, then the deferred ones.
func (db *DB) Plan() (runnable, deferred []StepRef) {
	for _, n := range db.boot.plan {
		runnable = append(runnable, n.ref)
	}
	for _, n := range db.boot.deferred {
		deferred = append(deferred, n.ref)
	}
	return runnable, deferred
}

// Migrate runs §5S.4 steps 8-9 (06 P4b): every runnable step in DAG order,
// each versioned step as one batch with its bookkeeping (and level stamp),
// each data step through its Run (DataStep), then the claim ledgers, the
// store generation, the sidecars, the books marker and the checkpoint's
// completion. A failed step halts with
// ErrMigrationFailed, wrapping a *StepError when the step itself failed;
// earlier steps stay committed and the attempt's checkpoint stays open.
func (db *DB) Migrate(ctx context.Context) error {
	st := db.boot
	if st == nil || !st.prepared {
		return errors.New("storage: Migrate before Prepare")
	}
	if st.migrated {
		return errors.New("storage: Migrate called twice")
	}
	st.migrated = true
	for _, n := range st.plan {
		st.opts.ExtendStart("storage.migration " + n.ref.String())
		var err error
		switch {
		case n.data != nil:
			// A data step labels and commits its own writes (its runner's
			// plan, chunk and state records), so no region wraps it here: an
			// outer region would win over the runner's (internal/bootseam).
			err = n.data.Run(ctx, db)
		case n.legacy != nil:
			// A legacy step's Up writes through the owner's LegacyRange
			// before its bookkeeping batch: the whole step is one row.
			err = db.labelled(labelStepBatch, func() error { return db.runLegacy(ctx, n) })
		default:
			err = db.runStep(ctx, n)
		}
		if err != nil {
			return &ErrMigrationFailed{Step: n.ref, Err: err}
		}
		if n.data == nil {
			st.versions[n.ref.Owner] = n.ref.Version
		}
		db.log.Info("storage: migration step applied", "step", n.ref.String(), "name", n.name())
	}
	return db.finish(ctx)
}

// runLegacy runs a books legacy step over the owner's own LegacyRange (not
// atomic, §5S.3.5 "Atomicity"), then its bookkeeping through the owner's
// VersionSource in its own batch.
func (db *DB) runLegacy(ctx context.Context, n *stepNode) (err error) {
	lr, _ := db.Legacy(n.owner.owner)
	if err := n.legacy.Up(ctx, lr); err != nil {
		return stepFailed(ctx, n.ref, err)
	}
	b := lr.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	step := AppliedStep{Version: n.ref.Version, Name: n.legacy.Name, AppliedAt: db.clock().UTC(), Build: db.boot.opts.Build}
	if err := n.owner.spec.VersionSource.Stage(ctx, lr, b, step); err != nil {
		return err
	}
	return b.Commit(ctx, Sync)
}

// runStep runs one versioned step: Up stages into an indexed batch over the
// owner's keyspace and claims, then the version, the applied record and, for
// a level step, the stamp join it, and the one batch commits.
func (db *DB) runStep(ctx context.Context, n *stepNode) (err error) {
	o := n.owner
	ks := db.storageKS()
	b := newBatch(ks, true)
	b.exclusive = true
	defer func() { err = errors.Join(err, b.Close()) }()
	tx := &MigrationTx{b: b, log: db.log.With("step", n.ref.String()), maxBytes: db.set.migMaxBytes, maxOps: db.set.migMaxOps}
	if own, ok := db.Keyspace(o.owner); ok {
		b.Bind(own)
		own.tx = tx
		tx.ks = own
	}
	if lr, ok := db.Legacy(o.owner); ok {
		b.Bind(lr)
		lr.tx = tx
		tx.legacy = lr
	}
	if err := n.mig.Up(ctx, tx); err != nil {
		return stepFailed(ctx, n.ref, err)
	}
	if b.err != nil {
		return stepFailed(ctx, n.ref, b.err) // a write the step staged was refused
	}
	step := AppliedStep{Version: n.mig.Version, Name: n.mig.Name, Compat: n.mig.Compat, HasCompat: true,
		RaisesLevel: n.mig.RaisesLevel, AppliedAt: db.clock().UTC(), Build: db.boot.opts.Build}
	if err := db.stageApplied(ctx, b, o, step); err != nil {
		return err
	}
	if n.mig.Compat == Breaking {
		return db.commitWithRaise(ctx, b, o, n.mig.RaisesLevel)
	}
	return db.labelled(labelStepBatch, func() error { return b.Commit(ctx, Sync) })
}

// finish is §5S.4 step 9: one batch with the claim ledgers and the store
// generation (each only when it changed), the books marker cleared when no
// books Breaking step is pending, and c/storage/attempt naming this boot's
// checkpoint; the sidecars rewritten from the stamps; the attempt's
// checkpoint completed; then c/storage/attempt deleted. A kill after the
// batch leaves the key, and the next boot's Prepare completes the record.
func (db *DB) finish(ctx context.Context) (err error) {
	st := db.boot
	now := db.clock().UTC()
	ks := db.storageKS()
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := db.stageLedgers(b, now, false); err != nil {
		return err
	}
	if st.epochNew {
		raw, err := enc(codecEpoch(), st.epoch)
		if err != nil {
			return err
		}
		if err := b.Set(epochKey(), raw); err != nil {
			return err
		}
	}
	if st.cpName != "" {
		raw, err := enc(codecAttempt(), attemptRecord{Checkpoint: st.cpName})
		if err != nil {
			return err
		}
		if err := b.Set(attemptKey(), raw); err != nil {
			return err
		}
	}
	for _, o := range sortedInfos(db.owners) {
		g := o.spec.LegacyGuard
		if g == nil || db.breakingDeferred(o.owner) {
			continue
		}
		lr, _ := db.Legacy(o.owner)
		if _, err := lr.Get(ctx, []byte(g.MarkerKey)); errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return err
		}
		b.Bind(lr)
		if err := g.Stage(ctx, lr, b, g.projection(st.stamps[o.levelOwner()]), false); err != nil {
			return fmt.Errorf("storage: clearing %s's migration marker: %w", o.owner, err)
		}
	}
	if err := db.labelled(labelFinishBatch, func() error { return b.Commit(ctx, Sync) }); err != nil {
		return fmt.Errorf("storage: step 9: %w", err)
	}
	st.epochNew = false
	if err := db.labelled(labelFinishSidecars, db.writeSidecars); err != nil {
		return err
	}
	if h := db.hooks.beforeComplete; h != nil {
		if err := h(); err != nil {
			return err
		}
	}
	if err := db.labelled(labelCheckpointDone, func() error { return db.completeCheckpoint(now) }); err != nil {
		return err
	}
	if st.cpName != "" {
		if err := db.labelled(labelAttemptClear, func() error { return ks.Delete(ctx, attemptKey()) }); err != nil {
			return fmt.Errorf("storage: clearing the completed attempt: %w", err)
		}
	}
	st.finished = true
	return nil
}

// breakingDeferred reports a deferred Breaking step of owner.
func (db *DB) breakingDeferred(o Owner) bool {
	for _, n := range db.boot.deferred {
		if n.ref.Owner == o && n.mig != nil && n.mig.Compat == Breaking {
			return true
		}
	}
	return false
}

// AppliedVersion is an owner's applied version after the boot.
func (db *DB) AppliedVersion(o Owner) int { return db.boot.versions[o] }

// Serves reports whether an owner may start: its applied version is at least
// MinVersionToServe (default: its latest registered version) (§5S.3.5
// "Barrier and serving").
func (db *DB) Serves(o Owner) bool {
	info := db.owners[o]
	if info == nil || !info.enabled {
		return false
	}
	want := info.spec.MinVersionToServe
	if want == 0 {
		want = highestVersion(info.spec)
	}
	return db.boot.versions[o] >= want
}
