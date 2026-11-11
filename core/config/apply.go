// file: core/config/apply.go
// version: 1.1.0
// guid: dd5734d4-3c90-4ba8-9e83-8eb5778ff55a
// last-edited: 2026-11-10

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/secrets"
)

// adminPerm is the permission ProtectAdmin asks Actor.Can for (05-config
// §1.3.7, SEC-6).
const adminPerm = "core:admin"

// change is one accepted key of a Patch.
type change struct {
	ck     ckey
	unset  bool            // remove the stored value: Unset, or a value equal to the default (C4)
	value  json.RawMessage // canonical, when !unset (non-secret keys)
	secret *secrets.Write  // secret keys
}

// commitNote is what Apply does for one section after it releases the store
// mutex: run the AfterCommit hooks when the stored (Pending) value changed,
// wake the OnChange subscribers when the effective value changed.
type commitNote struct {
	b                  *binding
	prevPend, nextPend *snapshot
	prevEff, nextEff   *snapshot
}

// Apply validates and commits a patch (05-config §1.4 step 8, §1.3.7):
// authorize every key (unknown, then section permission, then protection,
// then the operator lock), decode and Validate the candidate of every touched
// section, write the boot record when boot keys changed, Committer.Commit
// (one batch: the rows, the staged secrets, the audit record), publish the
// live keys, bump the generation, run the AfterCommit hooks and wake the
// OnChange subscribers. A refusal or a commit failure persists and publishes
// nothing and leaves the generation unchanged; after a commit failure the
// previous boot record is restored. A value equal to the default is staged
// as an unset (C4). A patch that changes nothing commits nothing.
//
// The store mutex is held from the candidate build through publish. The
// hooks and the wake-up run after it is released, on Apply's goroutine and
// with its ctx, so a hook may call Apply itself.
func (s *Store) Apply(ctx context.Context, p Patch, a Actor) (Result, error) {
	s.mu.Lock()
	res, notes, err := s.applyLocked(ctx, p, a)
	s.mu.Unlock()
	if err != nil {
		return Result{}, err
	}
	s.afterCommit(ctx, notes, &res)
	return res, nil
}

// applyLocked is Apply under s.mu.
func (s *Store) applyLocked(ctx context.Context, p Patch, a Actor) (Result, []commitNote, error) {
	switch {
	case s.closed:
		return Result{}, nil, errClosed
	case !s.attached:
		return Result{}, nil, errors.New("config: Apply before Attach")
	}
	changes, applied, err := s.plan(p, a)
	if err != nil {
		s.met.apply(outcomeOf(err))
		return Result{}, nil, err
	}
	gen := s.gen.Load() + 1
	now := s.now()
	newDB, newBoot, newBootAll := maps.Clone(s.dbVals), maps.Clone(s.bootVals), maps.Clone(s.boot)
	var (
		set     []Override
		unset   []Path
		writes  []secrets.Write
		bootChg []Path
		touched []*section
	)
	for _, c := range changes {
		pth := c.ck.p
		if !slices.Contains(touched, c.ck.k.sec) {
			touched = append(touched, c.ck.k.sec)
		}
		switch {
		case c.secret != nil:
			writes = append(writes, *c.secret)
		case c.ck.k.reload == ReloadBoot:
			// A quarantined entry never matches: the same bytes are written
			// again and validated, so Apply clears the quarantine or refuses,
			// and never succeeds while the key stays quarantined.
			cur, had := s.boot[pth]
			switch {
			case c.unset && had:
				delete(newBootAll, pth)
				delete(newBoot, pth)
				bootChg = append(bootChg, pth)
			case !c.unset && (!had || s.bootQuar[pth] != nil || !bytes.Equal(cur, c.value)):
				newBootAll[pth], newBoot[pth] = c.value, c.value
				bootChg = append(bootChg, pth)
			}
		default:
			// A quarantined row never matches either (see the boot case).
			row, had := s.db[pth]
			switch {
			case c.unset && had:
				delete(newDB, pth)
				unset = append(unset, pth)
			case !c.unset && (!had || s.quar[pth] != nil || !bytes.Equal(row.Value, c.value)):
				newDB[pth] = c.value
				set = append(set, Override{Path: pth, Value: c.value, Gen: gen, At: now, By: a.Subject})
			}
		}
	}
	if len(set)+len(unset)+len(writes)+len(bootChg) == 0 {
		s.met.apply(outcomeApplied)
		return Result{Generation: s.gen.Load(), Applied: applied, RestartPending: s.restartPending(applied)}, nil, nil
	}

	// Every touched section must decode and validate both as it will be
	// stored (Pending) and as it will be effective (live keys new, restart
	// and boot keys as of boot), because both are published.
	pendV := view{boot: newBoot, live: newDB, restart: newDB}
	effV := view{boot: s.bootAtLoad, live: newDB, restart: s.dbAtAttach}
	type cand struct{ eff, pend *built }
	cands := make(map[*section]cand, len(touched))
	var verrs []error
	for _, sec := range touched {
		pend, perr := s.build(sec, pendV)
		eff, eerr := s.build(sec, effV)
		switch {
		case perr != nil:
			verrs = append(verrs, perr)
		case eerr != nil:
			verrs = append(verrs, eerr)
		default:
			cands[sec] = cand{eff: eff, pend: pend}
		}
	}
	if len(verrs) > 0 {
		err := invalid(verrs...)
		s.met.apply(outcomeInvalid)
		return Result{}, nil, err
	}

	var prevRec []byte
	wroteBoot := false
	if len(bootChg) > 0 {
		var err error
		if prevRec, err = s.bootIO.read(); err == nil {
			err = s.bootIO.write(bootRecordFile{Format: bootRecordFormat, Gen: gen, Values: newBootAll})
		}
		if err != nil {
			s.met.apply(outcomeStorageError)
			return Result{}, nil, err
		}
		wroteBoot = true
	}
	cm := Commit{Set: set, Unset: unset, Secrets: writes, Gen: gen, Boot: bootChg, By: a.Subject}
	if err := s.committer.Commit(ctx, cm); err != nil {
		err = fmt.Errorf("config: commit: %w", err)
		if wroteBoot {
			if rerr := s.bootIO.restore(prevRec); rerr != nil {
				err = errors.Join(err, fmt.Errorf("config: restore the previous boot record: %w", rerr))
			}
		}
		s.met.apply(outcomeStorageError)
		return Result{}, nil, err
	}

	// Durable: publish.
	s.dbVals, s.bootVals, s.boot = newDB, newBoot, newBootAll
	for _, ov := range set {
		s.db[ov.Path] = ov
		delete(s.quar, ov.Path)
	}
	for _, pth := range unset {
		delete(s.db, pth)
		delete(s.quar, pth)
	}
	for _, pth := range bootChg {
		delete(s.bootQuar, pth)
	}
	s.syncQuarantineHealth()
	s.gen.Store(gen)
	notes := make([]commitNote, 0, len(touched))
	for _, sec := range touched {
		c := cands[sec]
		prevEff, nextEff, prevPend, nextPend := s.install(sec, c.eff, c.pend, gen, false)
		notes = append(notes, commitNote{b: sec.bind, prevPend: prevPend, nextPend: nextPend, prevEff: prevEff, nextEff: nextEff})
	}
	// After install, so a removed entry's secret is dropped and an added
	// entry's is read.
	s.noteSecretWrites(ctx, writes, a.Subject, now)
	s.syncFailClosed()
	s.reindex()
	s.met.setQuarantined(len(s.quar))
	s.met.apply(outcomeApplied)
	return Result{Generation: gen, Applied: applied, RestartPending: s.restartPending(applied)}, notes, nil
}

// restartPending lists the restart and boot keys of paths whose stored value
// is not yet effective.
func (s *Store) restartPending(paths []Path) []Path {
	idx := s.index.Load()
	var out []Path
	for _, p := range paths {
		if ks, ok := idx.byPath[p]; ok && ks.Reload != ReloadLive && ks.Pending {
			out = append(out, p)
		}
	}
	return out
}

// noteSecretWrites refreshes the Meta of the written secrets: through the
// resolver when there is one, else from the writes themselves. Every entry
// secret an Apply adds, drops or rebinds is among the writes (entryClears
// deletes it), so no other secret's Meta can have changed. A written secret
// no build holds any more (a removed entry's) loses its Meta.
func (s *Store) noteSecretWrites(ctx context.Context, writes []secrets.Write, by string, at time.Time) {
	if len(writes) == 0 {
		return
	}
	only := make([]Path, 0, len(writes))
	for _, w := range writes {
		only = append(only, Path(w.Name))
	}
	if s.resolver != nil {
		s.refreshSecrets(ctx, only)
		return
	}
	live := s.liveSecrets()
	for _, w := range writes {
		if _, ok := live[Path(w.Name)]; !ok {
			delete(s.secretMeta, Path(w.Name))
			continue
		}
		m := secrets.Meta{Name: w.Name, Set: !w.Delete, UpdatedAt: at, UpdatedBy: by}
		if !w.Delete {
			m.Source = secrets.SourceStored
		}
		s.secretMeta[Path(w.Name)] = &m
	}
}

// afterCommit runs the AfterCommit hooks of every section whose stored value
// changed, in section order, then wakes the OnChange subscribers of every
// section whose effective value changed.
func (s *Store) afterCommit(ctx context.Context, notes []commitNote, res *Result) {
	var perrs []error
	for _, n := range notes {
		if n.prevPend == nil || n.nextPend == nil || bytes.Equal(n.prevPend.raw, n.nextPend.raw) {
			continue
		}
		if errs := n.b.runHooks(ctx, n.prevPend.val, n.nextPend.val); len(errs) > 0 {
			perrs = append(perrs, &PostCommitError{Section: n.b.sec.path, Err: errors.Join(errs...)})
			s.met.postCommitFailure(n.b.sec.path)
			s.log.Error("config: an after-commit hook failed; the change stays committed",
				"section", n.b.sec.path, "err", errors.Join(errs...))
		}
	}
	for _, n := range notes {
		if n.prevEff != nil && n.nextEff != nil && n.prevEff != n.nextEff {
			n.b.notifyAll(n.prevEff, n.nextEff)
		}
	}
	if len(perrs) > 0 {
		res.PostCommit = errors.Join(perrs...)
	}
}

// meets reports whether a satisfies protection need (05-config §1.3.7).
func (s *Store) meets(a Actor, need Protection) bool {
	admin := a.Interactive && a.Can != nil && a.Can(adminPerm)
	switch need {
	case ProtectNone:
		return true
	case ProtectInteractive:
		return a.Interactive
	case ProtectAdmin:
		return admin
	case ProtectOwner:
		if !admin {
			return false
		}
		if s.o.OwnerSet != nil && s.o.OwnerSet() {
			return a.Owner
		}
		return true
	}
	return false // ProtectOperator: never through Apply
}

// plan authorizes and decodes p. The refusals come in a fixed order, each
// naming every key it refuses: unknown keys (passthrough paths included),
// then section permission, then protection, then the operator lock, then
// invalid values. applied lists every path of p, sorted.
func (s *Store) plan(p Patch, a Actor) (changes []change, applied []Path, err error) {
	unsetSet := map[Path]bool{}
	var both []error
	for _, pth := range p.Unset {
		if _, dup := p.Set[pth]; dup {
			both = append(both, &FieldError{Path: pth, Err: errors.New("both set and unset in one patch")})
		}
		unsetSet[pth] = true
	}
	applied = slices.Sorted(maps.Keys(p.Set))
	for pth := range unsetSet {
		if _, dup := p.Set[pth]; !dup {
			applied = append(applied, pth)
		}
	}
	slices.Sort(applied)

	keys := make(map[Path]ckey, len(applied))
	var unknown []string
	for _, pth := range applied {
		ck, ok := s.keyFor(pth)
		// A list entry's secret exists only while an entry names it in the
		// stored list: one for an ID no entry holds is unknown, so an entry
		// is added first and its secret set after (fail closed).
		if !ok || s.passthrough(pth) || (ck.k.list != nil && !ck.k.sec.pend.holds(pth)) {
			unknown = append(unknown, string(pth))
			continue
		}
		keys[pth] = ck
	}
	if len(unknown) > 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknownKey, strings.Join(unknown, ", "))
	}
	var forbidden []string
	for _, pth := range applied {
		perm := keys[pth].k.sec.permWrite
		if a.Can == nil || !a.Can(perm) {
			forbidden = append(forbidden, fmt.Sprintf("%s needs %s", pth, perm))
		}
	}
	if len(forbidden) > 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrForbidden, strings.Join(forbidden, "; "))
	}
	var refused []error
	for _, pth := range applied {
		if need := keys[pth].k.protect; !s.meets(a, need) {
			refused = append(refused, &ProtectedError{Path: pth, Need: need})
		}
	}
	if len(refused) > 0 {
		return nil, nil, errors.Join(refused...)
	}
	for _, pth := range applied {
		if op, ok := s.op[pth]; ok {
			// FailClosed (05-config §1.3.5): a value at least as
			// restrictive as the operator's, or an unset, is accepted; a
			// weaker one could never take effect.
			if failClosedApply(keys[pth].k, pth, p.Set[pth], unsetSet[pth], op) {
				continue
			}
			refused = append(refused, &LockedError{Path: pth, By: op.by})
			continue
		}
		// An entry an operator layer supplies reads only an operator-layer
		// secret (operatorOnly), so a stored one would never be used.
		if lk := keys[pth].k.list; lk != nil {
			if op, ok := s.op[lk.path("")]; ok {
				refused = append(refused, &LockedError{Path: pth, By: op.by})
			}
		}
	}
	if len(refused) > 0 {
		return nil, nil, errors.Join(refused...)
	}
	verrs := both
	for _, pth := range applied {
		ck := keys[pth]
		c, cerr := s.decodeChange(ck, p.Set[pth], unsetSet[pth], a.Subject)
		if cerr != nil {
			verrs = append(verrs, cerr)
			continue
		}
		changes = append(changes, c)
	}
	if len(verrs) > 0 {
		return nil, nil, invalid(verrs...)
	}
	clears, err := s.entryClears(changes, p.Set, unsetSet, a)
	if err != nil {
		return nil, nil, err
	}
	return append(changes, clears...), applied, nil
}

// entryClears deletes, in the same commit, the secrets of every list entry a
// list change does not keep as it was: an entry removed, renamed (the old ID
// goes), added (an ID not in the stored list, the IDs of a quarantined stored
// row included), or rebound (a secret:"bind" field changed, such as an OIDC
// provider's kind, issuer or client_id). 05-config and 05-authn say nothing
// of an entry's secret beyond its entry; a secret left stored would be
// handed to whatever entry next holds the ID, or to an entry now pointing at
// another identity provider, so config fails closed and a changed entry's
// secret always starts unset (seams.d/F3-03.md). Each deletion is a write to
// the derived key: refused when the actor does not meet its protection, when
// an operator layer sets it (the secrets store refuses to write a locked
// name), and when the patch also sets it. A patch that unsets it already
// deletes it.
func (s *Store) entryClears(changes []change, set map[Path]json.RawMessage, unset map[Path]bool, a Actor) ([]change, error) {
	var out []change
	var protected, locked, bad []error
	for _, c := range changes {
		lk := c.ck.k
		if len(lk.entrySecrets) == 0 {
			continue
		}
		next := c.value
		if c.unset {
			next = c.ck.def()
		}
		kept := lk.entryBindings(lk.sec.pend.vals[c.ck.p].raw)
		now := lk.entryBindings(next)
		ids := lk.entryIDsOf(lk.sec.pend.vals[c.ck.p].raw)
		if row, ok := s.db[c.ck.p]; ok {
			ids = append(ids, lk.entryIDsOf(row.Value)...)
		}
		ids = append(ids, lk.entryIDsOf(next)...)
		slices.Sort(ids)
		for _, id := range slices.Compact(ids) {
			was, wasOK := kept[id]
			is, isOK := now[id]
			if wasOK && isOK && was == is {
				continue
			}
			for _, d := range lk.entrySecrets {
				pth := d.path(id)
				if unset[pth] {
					continue
				}
				if _, both := set[pth]; both {
					bad = append(bad, &FieldError{Path: pth, Err: errors.New("set in the patch that adds, removes, renames or rebinds its list entry: set it in a later patch")})
					continue
				}
				if !s.meets(a, d.protect) {
					protected = append(protected, &ProtectedError{Path: pth, Need: d.protect})
					continue
				}
				if op, ok := s.op[pth]; ok {
					locked = append(locked, &LockedError{Path: pth, By: op.by})
					continue
				}
				out = append(out, change{ck: ckey{k: d, id: id, p: pth}, secret: &secrets.Write{Name: secrets.Name(pth), Delete: true, By: a.Subject}})
			}
		}
	}
	switch {
	case len(protected) > 0:
		return nil, errors.Join(protected...)
	case len(locked) > 0:
		return nil, errors.Join(locked...)
	case len(bad) > 0:
		return nil, invalid(bad...)
	}
	return out, nil
}

// decodeChange turns one patch entry into a change. Errors are *FieldError.
func (s *Store) decodeChange(ck ckey, raw json.RawMessage, isUnset bool, by string) (change, error) {
	p := ck.p
	if ck.k.reload == ReloadBoot && s.bootIO == nil {
		return change{}, &FieldError{Path: p, Err: errors.New("a boot key needs the boot record, and this app has no state directory")}
	}
	if ck.k.secret {
		if isUnset {
			return change{ck: ck, secret: &secrets.Write{Name: secrets.Name(p), Delete: true, By: by}}, nil
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return change{}, &FieldError{Path: p, Err: errors.New("a secret key takes its value as a JSON string")}
		}
		if v == "" {
			return change{}, &FieldError{Path: p, Err: errors.New("an empty secret: list the key in Unset to delete it")}
		}
		return change{ck: ck, secret: &secrets.Write{Name: secrets.Name(p), Value: secrets.NewValue(v), By: by}}, nil
	}
	if isUnset {
		return change{ck: ck, unset: true}, nil
	}
	c, err := ck.k.canonical(p, raw)
	if err != nil {
		return change{}, err
	}
	// C4 does not apply to a FailClosed key: a stored value equal to the
	// default (the most restrictive, for both declared keys) is what keeps a
	// weaker operator value, set now or later, from taking effect
	// (05-config §1.3.5).
	if bytes.Equal(c, ck.def()) && ck.k.failOrder == nil {
		return change{ck: ck, unset: true}, nil
	}
	return change{ck: ck, value: c}, nil
}
