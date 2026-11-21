// file: core/kernel/operator_config.go
// version: 0.1.0
// guid: 5b2e9d47-1c83-4f6a-a0d9-7e4c3b8f2a16
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

// ErrOperatorConfigReadOnly is what every write through an OperatorConfigAccess
// returns: it holds the operator layers alone, and a write belongs to the
// store a boot attaches.
var ErrOperatorConfigReadOnly = errors.New("kernel: operator config is read-only: it holds the operator layers (defaults, file, environment, flags) and no stored values")

// OperatorConfig is the app's configuration as its operator layers alone give
// it (defaults, the operator file, the environment and flags, with the app's
// core defaults): every core section and every compiled module's sections are
// registered and loaded exactly as config check loads them (CheckConfig), and
// no stored value is read. It opens no store, reads no boot record and creates
// no directory, so a command that never boots the App (a CLI subcommand, or a
// read before app.New) sees the operator's settings without the boot's side
// effects (name coined by F3-07). A retired name that would refuse a boot
// refuses here too, as the *config.RetiredNameError. The secrets it serves are
// the operator values of secret keys the module that declares
// Manifest.AppConfig admits; nothing else is reachable without the secret
// store. The caller closes it.
func OperatorConfig(ctx context.Context, c ConfigCheck) (*OperatorConfigAccess, error) {
	st, lerr, err := loadOperatorLayers(ctx, c)
	if err != nil {
		return nil, err
	}
	if lerr != nil {
		return nil, errors.Join(fmt.Errorf("kernel: operator config: %w", lerr), st.Close(ctx))
	}
	a := &OperatorConfigAccess{store: st, ops: map[secrets.Name]config.OperatorValue{}}
	for _, m := range c.Modules {
		if ac := m.Manifest().AppConfig; ac != nil {
			a.admit = ac.Secrets
			break
		}
	}
	for p, v := range st.OperatorSecrets() {
		a.ops[secrets.Name(p)] = v
	}
	return a, nil
}

// OperatorConfigAccess is module.ConfigAccess over the operator layers alone
// (OperatorConfig). The reads pass through to the loaded store; Apply,
// PutSecret and DeleteSecret return ErrOperatorConfigReadOnly; Secret and
// ListSecrets answer from the operator values of admitted secret keys.
type OperatorConfigAccess struct {
	store *config.Store
	admit func(secrets.Name) bool
	ops   map[secrets.Name]config.OperatorValue
	once  sync.Once
	cerr  error
}

var _ module.ConfigAccess = (*OperatorConfigAccess)(nil)

func (a *OperatorConfigAccess) Lookup(p config.Path) (config.KeyState, bool) {
	return a.store.Lookup(p)
}
func (a *OperatorConfigAccess) Keys() iter.Seq2[config.Path, config.KeyState] { return a.store.Keys() }
func (a *OperatorConfigAccess) Generation() uint64                            { return a.store.Generation() }

// OnCommit registers fn with the store; nothing commits through an operator
// view, so it never fires.
func (a *OperatorConfigAccess) OnCommit(fn func(gen uint64)) { a.store.OnCommit(fn) }

func (a *OperatorConfigAccess) PassthroughValues() iter.Seq2[config.Path, config.OperatorValue] {
	return a.store.PassthroughValues()
}

func (a *OperatorConfigAccess) Apply(context.Context, config.Patch, config.Actor) (config.Result, error) {
	return config.Result{}, ErrOperatorConfigReadOnly
}

// Secret is the operator value of an admitted secret key, with the Source the
// layer gave it; an admitted name the operator did not set is ErrNotSet with
// Meta.Set false, as the secret store reports an unset name.
func (a *OperatorConfigAccess) Secret(_ context.Context, name secrets.Name) (secrets.Value, secrets.Meta, error) {
	if a.admit == nil || !a.admit(name) {
		return secrets.Value{}, secrets.Meta{}, secrets.ErrOutOfScope
	}
	v, ok := a.ops[name]
	if !ok {
		return secrets.Value{}, secrets.Meta{Name: name}, secrets.ErrNotSet
	}
	text, src, err := operatorSecret(name, v)
	if err != nil {
		return secrets.Value{}, secrets.Meta{}, err
	}
	return secrets.NewValue(text), secrets.Meta{Name: name, Set: true, Source: src}, nil
}

// ListSecrets is the Meta of every admitted operator secret under prefix,
// sorted by name.
func (a *OperatorConfigAccess) ListSecrets(_ context.Context, prefix string) ([]secrets.Meta, error) {
	if a.admit == nil {
		return nil, secrets.ErrOutOfScope
	}
	var out []secrets.Meta
	for n, v := range a.ops {
		if !strings.HasPrefix(string(n), prefix) || !a.admit(n) {
			continue
		}
		_, src, err := operatorSecret(n, v)
		if err != nil {
			return nil, err
		}
		out = append(out, secrets.Meta{Name: n, Set: true, Source: src})
	}
	slices.SortFunc(out, func(x, y secrets.Meta) int { return strings.Compare(string(x.Name), string(y.Name)) })
	return out, nil
}

func (a *OperatorConfigAccess) PutSecret(context.Context, secrets.Name, secrets.Value, string) (secrets.Meta, error) {
	return secrets.Meta{}, ErrOperatorConfigReadOnly
}

func (a *OperatorConfigAccess) DeleteSecret(context.Context, secrets.Name, string) error {
	return ErrOperatorConfigReadOnly
}

// Close closes the loaded store; later calls return the first result.
func (a *OperatorConfigAccess) Close(ctx context.Context) error {
	a.once.Do(func() { a.cerr = a.store.Close(ctx) })
	return a.cerr
}

// operatorSecret decodes one operator secret value, with handOperatorSecrets'
// source rule (a flag cannot carry a secret; config refuses one at Load).
func operatorSecret(name secrets.Name, v config.OperatorValue) (string, secrets.Source, error) {
	var text string
	if err := json.Unmarshal(v.Value, &text); err != nil {
		return "", 0, fmt.Errorf("kernel: operator config: the operator value of %s (%s) is not a string", name, v.By)
	}
	switch v.Src {
	case config.SourceEnv:
		return text, secrets.SourceEnv, nil
	case config.SourceFile:
		return text, secrets.SourceFile, nil
	}
	return "", 0, fmt.Errorf("kernel: operator config: %s supplies the secret %s; set it in the environment (or its _FILE variant) or the config file instead", v.By, name)
}
