// file: internal/books/appconfig/write.go
// version: 1.6.0
// guid: 1e5a7c93-4d08-4f26-b3a9-8c0d2e6f1b74
// last-edited: 2026-11-19

package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// internalActor is who Mutate writes as: the process itself, which holds every
// permission. A key an operator layer sets is still refused (core's lock), and
// ProtectOperator keys are never writable through Apply at all.
var internalActor = config.InternalActor

// adminPerm is the permission ProtectAdmin asks for (core/config apply.go); the
// Engine asks it before writing a secret outside Apply.
const adminPerm = "core:admin"

// Mutate applies fn to a copy of the projection and writes the difference. It is
// config.Mutate's body once the Engine is installed: the callers hold no error
// today, so a refusal is returned for config.Mutate to report, and the projection
// stays as it was.
func (e *Engine) Mutate(fn func(*config.Config)) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	e.mu.RLock()
	before := e.cur
	e.mu.RUnlock()
	after := before
	fn(&after)
	set, err := e.diff(before, after)
	if err != nil {
		return err
	}
	if len(set) == 0 {
		return nil
	}
	_, err = e.write(context.Background(), internalActor, config.Update{Set: set}, false)
	return err
}

// Update is PUT /api/v1/config's write: HEAD's flat keys to their new values,
// committed all or none for the keys Apply owns.
func (e *Engine) Update(ctx context.Context, a config.Actor, u config.Update) (config.UpdateResult, error) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.write(ctx, a, u, true)
}

// diff lists the rows whose value differs between before and after, as an Update's
// Set. A changed field no row covers is an error: nothing would store it.
func (e *Engine) diff(before, after config.Config) (map[string]json.RawMessage, error) {
	set := map[string]json.RawMessage{}
	covered, below := map[string]bool{}, map[string]bool{}
	for _, p := range e.rows {
		covered[p.row.Head] = true
		for i := 1; i < len(p.segs); i++ {
			below[strings.Join(p.segs[:i], ".")] = true
		}
		bf, ok1 := e.own(before, p)
		af, ok2 := e.own(after, p)
		if !ok1 || !ok2 || reflect.DeepEqual(bf.Interface(), af.Interface()) {
			continue
		}
		raw, err := json.Marshal(af.Interface())
		if err != nil {
			return nil, fmt.Errorf("appconfig: encoding %s: %w", p.row.Head, err)
		}
		set[p.row.Head] = raw
	}
	if miss := uncovered(reflect.ValueOf(&before).Elem(), reflect.ValueOf(&after).Elem(), nil, covered, below); len(miss) > 0 {
		return nil, fmt.Errorf("%w: %s", coreconfig.ErrUnknownKey, strings.Join(miss, ", "))
	}
	return set, nil
}

// write is Update's body under writeMu. hooks is true for the API write, which
// runs the UpdateHooks; Mutate's internal writes do not.
func (e *Engine) write(ctx context.Context, a config.Actor, u config.Update, hooks bool) (config.UpdateResult, error) {
	e.mu.RLock()
	before := e.cur
	e.mu.RUnlock()
	// HEAD's first rule, before every other check: nothing through the API changes
	// who the owner is or how the owner signs in unless the owner asks, and with
	// no owner set, nobody can.
	if hooks {
		if err := e.ownerRootRefusal(a, before, u); err != nil {
			return config.UpdateResult{}, err
		}
	}
	patch := coreconfig.Patch{Set: map[coreconfig.Path]json.RawMessage{}}
	var secretSets, secretClears []secretOp
	resSet, resUnset := map[string]json.RawMessage{}, []string{}
	var adminHeads, aiHeads []string
	var unknown, readOnly []string
	var locked []*coreconfig.LockedError
	locks := e.Locks()
	var invalid []error
	byPath := map[coreconfig.Path]string{}
	foldVals, rootsVals := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	aiVals := map[string]json.RawMessage{}     // ai_backend.<name> -> value
	memberVals := map[string]json.RawMessage{} // a member row's head -> its canonical value
	add := func(head string, raw json.RawMessage, clear bool) {
		p, ok := e.byHead[head]
		if ok && p.kind == kindAI {
			adminHeads = append(adminHeads, head)
			if clear {
				invalid = append(invalid, &coreconfig.FieldError{Path: coreconfig.Path(head), Err: errors.New("cannot be cleared: set the mode or the model you want")})
				return
			}
			aiVals[strings.TrimPrefix(head, "ai_backend.")] = raw
			return
		}
		if ok && p.kind == kindMember {
			// A member of a map-valued core key: checked against its Config type,
			// converted, and merged into the map below. Clearing one is its zero
			// value, as for any field stored inside a core value.
			if p.row.Admin {
				adminHeads = append(adminHeads, head)
			}
			var probe config.Config
			f, has := fieldAt(&probe, p.segs)
			if !has {
				unknown = append(unknown, head)
				return
			}
			if clear {
				raw = mustMarshal(f.Interface())
			} else if json.Unmarshal(raw, f.Addr().Interface()) != nil {
				invalid = append(invalid, &coreconfig.FieldError{Path: coreconfig.Path(head), Err: errors.New("the value does not fit the setting")})
				return
			}
			out, _, err := cutover.Convert(p.target.Convert, raw)
			if err != nil {
				invalid = append(invalid, &coreconfig.FieldError{Path: coreconfig.Path(head), Err: err})
				return
			}
			memberVals[head] = out
			return
		}
		if ok && (e.foldHeads[head] || p.kind != kindKey) {
			// A field stored inside a core list: checked against its Config type
			// here, folded into the list below. Clearing one is its zero value.
			// The gate is the Engine's own: the lists are shared by several
			// HEAD fields, so core's per-key policy alone is not asked.
			if p.row.Admin {
				adminHeads = append(adminHeads, head)
			}
			var probe config.Config
			f, has := fieldAt(&probe, p.segs)
			if !has {
				unknown = append(unknown, head)
				return
			}
			if clear {
				raw = mustMarshal(f.Interface())
			} else if json.Unmarshal(raw, f.Addr().Interface()) != nil {
				invalid = append(invalid, &coreconfig.FieldError{Path: coreconfig.Path(head), Err: errors.New("the value does not fit the setting")})
				return
			}
			switch {
			case p.kind == kindRoots:
				rootsVals[head] = raw
				return
			case p.kind == kindFold:
				foldVals[head] = raw
				return
			}
			foldVals[head] = raw // the allow-list also has a key of its own: fall through
		}
		switch {
		case !ok:
			unknown = append(unknown, head)
			return
		case p.residual:
			if by, isLocked := locks[head]; isLocked {
				// The blob is not core's store, so core's lock does not stop this
				// write: an operator-set value is refused here instead of stored
				// and overridden at the next boot, which HEAD did.
				locked = append(locked, &coreconfig.LockedError{Path: coreconfig.Path(head), By: by})
				return
			}
			if p.row.Admin {
				adminHeads = append(adminHeads, head)
			}
			if clear {
				resUnset = append(resUnset, head)
				return
			}
			var probe config.Config
			if f, ok := fieldAt(&probe, p.segs); !ok || json.Unmarshal(raw, f.Addr().Interface()) != nil {
				invalid = append(invalid, &coreconfig.FieldError{Path: coreconfig.Path(head), Err: errors.New("the value does not fit the setting")})
				return
			}
			resSet[head] = raw
			return
		case p.target.Kind == cutover.TargetPassthrough:
			readOnly = append(readOnly, head)
			return
		}
		path := coreconfig.Path(p.target.Path)
		if p.target.Kind == cutover.TargetSecret {
			if by, isLocked := locks[head]; isLocked {
				// An operator-supplied secret locks the setting. Core's lock covers a
				// registered key; a passthrough secret has no key for it to cover.
				locked = append(locked, &coreconfig.LockedError{Path: path, By: by})
				return
			}
			var s string
			if !clear {
				if err := json.Unmarshal(raw, &s); err != nil {
					invalid = append(invalid, &coreconfig.FieldError{Path: path, Err: errors.New("a secret is a string")})
					return
				}
				if hooks && maskCheckedHeads[head] && maskShaped(s) {
					invalid = append(invalid, &coreconfig.FieldError{Path: path, Err: errMaskShaped(head)})
					return
				}
			}
			if _, registered := e.acc.Lookup(path); !registered {
				op := secretOp{head: head, name: secrets.Name(p.target.Path), value: s}
				if clear || s == "" {
					secretClears = append(secretClears, op)
				} else {
					secretSets = append(secretSets, op)
				}
				return
			}
			if s == "" {
				clear = true
			} else {
				raw = mustMarshal(s)
			}
		}
		byPath[path] = head
		if clear {
			patch.Unset = append(patch.Unset, path)
			return
		}
		out, _, err := cutover.Convert(p.target.Convert, raw)
		if errors.Is(err, cutover.ErrNotStaged) {
			readOnly = append(readOnly, head)
			return
		}
		if err != nil {
			invalid = append(invalid, &coreconfig.FieldError{Path: path, Err: err})
			return
		}
		patch.Set[path] = out
	}
	for _, head := range slices.Sorted(maps.Keys(u.Set)) {
		add(head, u.Set[head], false)
	}
	for _, head := range u.Unset {
		if _, dup := u.Set[head]; dup {
			invalid = append(invalid, &coreconfig.FieldError{Path: coreconfig.Path(head), Err: errors.New("both set and unset in one update")})
			continue
		}
		add(head, nil, true)
	}
	also := map[coreconfig.Path][]string{}
	if err := e.foldInto(ctx, &patch, byPath, also, foldVals, rootsVals, secretSets); err != nil {
		invalid = append(invalid, err)
	}
	if err := e.memberInto(&patch, byPath, also, memberVals); err != nil {
		invalid = append(invalid, err)
	}
	// The ai_backend.* fields are one write of the ai_endpoints list.
	checked := u
	if len(aiVals) > 0 {
		out, err := e.foldAIBackend(ctx, aiVals, resSet)
		if err != nil {
			invalid = append(invalid, err)
		} else if out != nil {
			resSet["ai_endpoints"] = out
			checked.Set = maps.Clone(u.Set)
			if checked.Set == nil {
				checked.Set = map[string]json.RawMessage{}
			}
			checked.Set["ai_endpoints"] = out
			aiHeads = slices.Sorted(maps.Keys(aiVals))
		}
	}
	// Credentials follow their URL (egress.go): beyond HEAD parity. It sees the
	// list the write ends with, so a model or URL folded in is checked too.
	invalid = append(invalid, egressViolations(before, checked)...)
	if hooks {
		invalid = append(invalid, endpointsProblems(u)...)
		invalid = append(invalid, maskedCredentialProblems(u)...)
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return config.UpdateResult{}, fmt.Errorf("%w: %s", coreconfig.ErrUnknownKey, unknownKeysMessage(unknown))
	}
	if len(readOnly) > 0 {
		slices.Sort(readOnly)
		return config.UpdateResult{}, fmt.Errorf("%w: %s", config.ErrKeyReadOnly, strings.Join(readOnly, ", "))
	}
	if len(locked) > 0 {
		slices.SortFunc(locked, func(a, b *coreconfig.LockedError) int { return strings.Compare(string(a.Path), string(b.Path)) })
		return config.UpdateResult{}, locked[0]
	}
	if len(invalid) > 0 {
		return config.UpdateResult{}, headErr(errors.Join(coreconfig.ErrInvalid, errors.Join(invalid...)), byPath)
	}
	needAdmin := append(secretHeads(append(slices.Clone(secretSets), secretClears...)), adminHeads...)
	// A program path is judged against the server's filesystem, so who may ask is
	// decided first: a caller short of the admin role is refused on the key alone
	// and no check runs for them (the answer would say what exists and its mode).
	execHeads := execPathHeads(before, u)
	needAdmin = append(needAdmin, execHeads...)
	applyAs := a
	if !(a.Interactive && a.Can != nil && a.Can(adminPerm)) {
		inWindow := firstRunMay(a, u)
		if len(needAdmin) > 0 && !inWindow {
			slices.Sort(needAdmin)
			return config.UpdateResult{}, fmt.Errorf("%w: %s needs %s", coreconfig.ErrForbidden, strings.Join(needAdmin, ", "), adminPerm)
		}
		if inWindow {
			// The first-run window: the Engine has authorized an update made only
			// of the setup wizard's keys, so core's own protection (a library root
			// is an admin key; so may be any other key of the list) is answered for
			// exactly this write, as Mutate's internal actor is. An update that
			// names one key outside the list is not elevated: core sees the caller
			// as they are and refuses every admin key in it.
			applyAs = firstRunActor(a)
		}
	}
	// HEAD ran Config.Validate on every PUT candidate before it wrote anything, and
	// core's per-key schema does not state all of its checks (a backup codec, the
	// naming patterns, the dedup ladder, a path's parent directory ...). Only a
	// caller authorized for the keys reaches it: some checks look at the filesystem.
	if hooks {
		if err := e.validateCandidate(before, u, resSet["ai_endpoints"]); err != nil {
			return config.UpdateResult{}, headErr(errors.Join(coreconfig.ErrInvalid, err), byPath)
		}
	}
	// A program the server runs must be a path an unprivileged user cannot swap.
	// Only a caller already authorized to write it reaches the filesystem.
	if v := execPathViolations(before, u); len(v) > 0 {
		return config.UpdateResult{}, headErr(errors.Join(coreconfig.ErrInvalid, errors.Join(v...)), byPath)
	}
	// Update is all or nothing: its steps commit in different stores (core's
	// rows, the secret store, the residual blob), so what each would replace is
	// read first, before anything is written, and a failing step undoes the ones
	// before it (compensate). The residual save is last because nothing can fail
	// after it that needs undoing (a single batch, so a failed save wrote nothing),
	// which is why it has no snapshot or undo of its own.
	undo, err := e.snapshotForUndo(ctx, patch, secretSets, secretClears)
	if err != nil {
		return config.UpdateResult{}, err
	}
	var res coreconfig.Result
	genBefore := e.acc.Generation()
	var done []undoStep
	fail := func(cause error) (config.UpdateResult, error) {
		return config.UpdateResult{}, e.compensate(ctx, done, cause)
	}
	if len(patch.Set)+len(patch.Unset) > 0 {
		var err error
		if res, err = e.acc.Apply(ctx, patch, applyAs); err != nil {
			return config.UpdateResult{}, headErr(err, byPath)
		}
		if res.Generation != genBefore {
			done = append(done, undo.keysStep(e, applyAs))
		}
	}
	for _, op := range secretSets {
		if _, err := e.acc.PutSecret(ctx, op.name, secrets.NewValue(op.value), a.Subject); err != nil {
			return fail(fmt.Errorf("appconfig: storing %s: %w", op.head, err))
		}
		done = append(done, undo.secret(e, op.name, op.head, a.Subject))
		res.Applied = append(res.Applied, coreconfig.Path(op.name))
		byPath[coreconfig.Path(op.name)] = op.head
	}
	for _, op := range secretClears {
		if err := e.acc.DeleteSecret(ctx, op.name, a.Subject); err != nil {
			return fail(fmt.Errorf("appconfig: clearing %s: %w", op.head, err))
		}
		done = append(done, undo.secret(e, op.name, op.head, a.Subject))
		res.Applied = append(res.Applied, coreconfig.Path(op.name))
		byPath[coreconfig.Path(op.name)] = op.head
	}
	if len(resSet)+len(resUnset) > 0 {
		if err := e.res.Save(ctx, resSet, resUnset); err != nil {
			return fail(fmt.Errorf("appconfig: saving the residual settings: %w", err))
		}
		for h := range resSet {
			res.Applied = append(res.Applied, coreconfig.Path(h))
			byPath[coreconfig.Path(h)] = h
			if h == "ai_endpoints" && len(aiHeads) > 0 {
				// The list was written for the fields; name them, not the list.
				byPath[coreconfig.Path(h)] = "ai_backend." + aiHeads[0]
				for _, n := range aiHeads[1:] {
					also[coreconfig.Path(h)] = append(also[coreconfig.Path(h)], "ai_backend."+n)
				}
			}
		}
		for _, h := range resUnset {
			res.Applied = append(res.Applied, coreconfig.Path(h))
			byPath[coreconfig.Path(h)] = h
		}
	}
	// An Update that leaves every value as it is is a no-op, not an error (HEAD
	// answered 200 to an idempotent save, and a form that re-sends every field
	// is one): core committed nothing and moved no generation, so nothing was
	// applied, no hook has anything to start, and the current generation is the
	// answer.
	if res.Generation == genBefore && len(secretSets)+len(secretClears)+len(resSet)+len(resUnset) == 0 {
		return config.UpdateResult{Generation: genBefore}, nil
	}
	// An Apply re-projects through OnCommit; a secret write outside Apply moves
	// nothing core sees, so project again here. Serialised by mu, so a hook's
	// rebuild and this one never publish out of order.
	if err := e.Rebuild(ctx); err != nil {
		return config.UpdateResult{}, fmt.Errorf("appconfig: the write committed but re-projecting failed: %w", err)
	}
	out := config.UpdateResult{Generation: res.Generation, PostCommit: res.PostCommit}
	if hooks {
		after := e.Snapshot()
		var errs []error
		if res.PostCommit != nil {
			errs = append(errs, res.PostCommit)
		}
		for _, h := range e.hooks {
			ops, err := runHook(ctx, h, before, after)
			if err != nil {
				errs = append(errs, &coreconfig.PostCommitError{Section: coreconfig.Path(h.name), Err: err})
			}
			for k, v := range ops {
				if out.Ops == nil {
					out.Ops = map[string]string{}
				}
				out.Ops[k] = v
			}
		}
		out.PostCommit = errors.Join(errs...)
	}
	for _, p := range res.Applied {
		out.Applied = append(out.Applied, byPath[p])
		out.Applied = append(out.Applied, also[p]...)
	}
	for _, p := range res.RestartPending {
		if h, ok := byPath[p]; ok {
			out.RestartPending = append(out.RestartPending, h)
		}
	}
	return out, nil
}

// runHook runs one UpdateHook; a panic is the hook's error, so one bad hook
// neither skips the next nor turns a committed write into a 500.
func runHook(ctx context.Context, h namedHook, before, after config.Config) (ops map[string]string, err error) {
	defer func() {
		if r := recover(); r != nil {
			ops, err = nil, fmt.Errorf("update hook %s panicked: %v", h.name, r)
		}
	}()
	return h.fn(ctx, before, after)
}

// secretOp is one secret-store write the update makes outside Apply.
type secretOp struct {
	head  string
	name  secrets.Name
	value string
}

func secretHeads(ops []secretOp) []string {
	var hs []string
	for _, o := range ops {
		hs = append(hs, o.head)
	}
	return hs
}

// headErr rewrites core's errors to HEAD's names: a path the store reports is the
// canonical one, and the caller knows only the flat key. The sentinels and their
// types survive, so errors.Is and errors.As work as for core.
func headErr(err error, byPath map[coreconfig.Path]string) error {
	if err == nil {
		return nil
	}
	name := func(p coreconfig.Path) coreconfig.Path {
		if h, ok := byPath[p]; ok {
			return coreconfig.Path(h)
		}
		return p
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range multi.Unwrap() {
			out = append(out, headErr(e, byPath))
		}
		return errors.Join(out...)
	}
	switch e := err.(type) {
	case *coreconfig.LockedError:
		c := *e
		c.Path = name(e.Path)
		return &c
	case *coreconfig.ProtectedError:
		c := *e
		c.Path = name(e.Path)
		return &c
	case *coreconfig.FieldError:
		c := *e
		c.Path = name(e.Path)
		return &c
	}
	msg := err.Error()
	for p, h := range byPath {
		msg = strings.ReplaceAll(msg, string(p), h)
	}
	for _, sentinel := range []error{coreconfig.ErrUnknownKey, coreconfig.ErrForbidden} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("%w%s", sentinel, strings.TrimPrefix(msg, sentinel.Error()))
		}
	}
	return err
}

func mustMarshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// validateCandidate is Config.Validate over the Config this update would
// produce: the projection as it stands, with each named field set to the typed
// value the update carries (the HEAD field a row fills, by the same decode the
// update was already checked with). It reports only the problems the update
// adds: one the stored state already has is not blamed on a save that does not
// touch it, so an update that repairs a bad value is never refused for the bad
// value it replaces, and an unset (a return to the default, which is valid) is
// judged by what remains. At boot the projection passed Validate, so for every
// stored state a running server can be in this is HEAD's rule exactly.
//
// aiOut is the ai_endpoints list an ai_backend.* write folds into, already
// built; the candidate carries it, not the five scalar fields.
func (e *Engine) validateCandidate(before config.Config, u config.Update, aiOut json.RawMessage) error {
	cand := before.Clone()
	for head, raw := range u.Set {
		p, ok := e.byHead[head]
		if !ok || p.kind == kindAI {
			continue
		}
		if f, has := fieldAt(cand, p.segs); has && f.CanSet() {
			// A value that does not fit was refused above; this decode cannot differ.
			_ = json.Unmarshal(raw, f.Addr().Interface())
		}
	}
	// An unset returns the key to the value no row gives it, which is the base.
	if len(u.Unset) > 0 && e.base != nil {
		def := e.base()
		for _, head := range u.Unset {
			p, ok := e.byHead[head]
			if !ok || p.kind == kindAI {
				continue
			}
			dst, hasDst := fieldAt(cand, p.segs)
			src, hasSrc := fieldAt(&def, p.segs)
			if hasDst && hasSrc && dst.CanSet() {
				_ = json.Unmarshal(mustMarshal(src.Interface()), dst.Addr().Interface())
			}
		}
	}
	if aiOut != nil {
		if f, has := fieldAt(cand, []string{"ai_endpoints"}); has {
			_ = json.Unmarshal(aiOut, f.Addr().Interface())
		}
	}
	found := cand.Problems()
	if len(found) == 0 {
		return nil
	}
	had := map[string]bool{}
	for _, m := range before.Clone().Problems() {
		had[m] = true
	}
	// A key the write names is judged on its own: replacing one bad value with
	// another bad value is not a repair, even though the problem reads the same.
	touched := map[string]bool{}
	for head := range u.Set {
		touched[head] = true
	}
	for _, head := range u.Unset {
		touched[head] = true
	}
	var errs []error
	for _, m := range found {
		path, _, _ := strings.Cut(m, " ")
		if had[m] && !touched[path] {
			continue
		}
		if _, known := e.byHead[path]; !known {
			path = ""
		}
		errs = append(errs, &coreconfig.FieldError{Path: coreconfig.Path(path), Err: errors.New(m)})
	}
	return errors.Join(errs...)
}
