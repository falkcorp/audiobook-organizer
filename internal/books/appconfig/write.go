// file: internal/books/appconfig/write.go
// version: 1.0.0
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
var internalActor = config.Actor{
	Subject: "config:mutate", Interactive: true, Owner: true,
	Can: func(string) bool { return true },
}

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
		bf, ok1 := fieldAt(&before, p.segs)
		af, ok2 := fieldAt(&after, p.segs)
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
	patch := coreconfig.Patch{Set: map[coreconfig.Path]json.RawMessage{}}
	var secretSets, secretClears []secretOp
	var unknown, readOnly []string
	var invalid []error
	byPath := map[coreconfig.Path]string{}
	add := func(head string, raw json.RawMessage, clear bool) {
		p, ok := e.byHead[head]
		switch {
		case !ok:
			unknown = append(unknown, head)
			return
		case p.target.Kind == cutover.TargetPassthrough:
			readOnly = append(readOnly, head)
			return
		}
		path := coreconfig.Path(p.target.Path)
		if p.target.Kind == cutover.TargetSecret {
			var s string
			if !clear {
				if err := json.Unmarshal(raw, &s); err != nil {
					invalid = append(invalid, &coreconfig.FieldError{Path: path, Err: errors.New("a secret is a string")})
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
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return config.UpdateResult{}, fmt.Errorf("%w: %s", coreconfig.ErrUnknownKey, strings.Join(unknown, ", "))
	}
	if len(readOnly) > 0 {
		slices.Sort(readOnly)
		return config.UpdateResult{}, fmt.Errorf("%w: %s", config.ErrKeyReadOnly, strings.Join(readOnly, ", "))
	}
	if len(invalid) > 0 {
		return config.UpdateResult{}, headErr(errors.Join(coreconfig.ErrInvalid, errors.Join(invalid...)), byPath)
	}
	if len(secretSets)+len(secretClears) > 0 && !(a.Interactive && a.Can != nil && a.Can(adminPerm)) {
		return config.UpdateResult{}, fmt.Errorf("%w: %s needs %s", coreconfig.ErrForbidden, secretHeads(append(slices.Clone(secretSets), secretClears...)), adminPerm)
	}
	var res coreconfig.Result
	if len(patch.Set)+len(patch.Unset) > 0 {
		var err error
		if res, err = e.acc.Apply(ctx, patch, a); err != nil {
			return config.UpdateResult{}, headErr(err, byPath)
		}
	}
	for _, op := range secretSets {
		if _, err := e.acc.PutSecret(ctx, op.name, secrets.NewValue(op.value), a.Subject); err != nil {
			return e.partial(ctx, res, byPath, fmt.Errorf("appconfig: storing %s: %w", op.head, err))
		}
		res.Applied = append(res.Applied, coreconfig.Path(op.name))
		byPath[coreconfig.Path(op.name)] = op.head
	}
	for _, op := range secretClears {
		if err := e.acc.DeleteSecret(ctx, op.name, a.Subject); err != nil {
			return e.partial(ctx, res, byPath, fmt.Errorf("appconfig: clearing %s: %w", op.head, err))
		}
		res.Applied = append(res.Applied, coreconfig.Path(op.name))
		byPath[coreconfig.Path(op.name)] = op.head
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

func secretHeads(ops []secretOp) string {
	var hs []string
	for _, o := range ops {
		hs = append(hs, o.head)
	}
	slices.Sort(hs)
	return strings.Join(hs, ", ")
}

// partial reports a secret write that failed after Apply committed: what
// committed stays, and the projection is rebuilt to say so.
func (e *Engine) partial(ctx context.Context, res coreconfig.Result, byPath map[coreconfig.Path]string, cause error) (config.UpdateResult, error) {
	_ = e.Rebuild(ctx)
	return config.UpdateResult{Generation: res.Generation}, cause
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
