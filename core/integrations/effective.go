// file: core/integrations/effective.go
// version: 1.0.0
// guid: 4cea2446-663d-448f-b1e5-1ba9259adfc6
// last-edited: 2026-11-11

package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// The effective view (M1.4 step 2): the stored row plus the operator
// overlay, computed in memory at Load, at every Put or Delete and at every
// config hot swap, and never persisted (SIMP2-5).

// opPaths parses the passthrough values config hands the Registry: each
// path is integrations.<kind>.<instance>.<field> (05-config §2.3's secret
// grammar, so an instance segment may hold '-'; config's own path grammar
// does not apply). A path of any other shape is dropped and logged: config
// owns no passthrough path, so nothing else would report it.
func (r *Registry) opPaths(vals iter.Seq2[config.Path, config.OperatorValue]) map[Ref]map[string]config.OperatorValue {
	out := map[Ref]map[string]config.OperatorValue{}
	if vals == nil {
		return out
	}
	for p, v := range vals {
		segs := strings.Split(string(p), ".")
		if len(segs) != 4 || segs[0] != "integrations" {
			r.log.Warn("integrations: an operator value whose path is not integrations.<kind>.<instance>.<field> was ignored", "path", string(p), "by", v.By)
			continue
		}
		ref := Ref{Kind: Kind(segs[1]), Instance: InstanceID(segs[2])}
		if err := ref.Validate(); err != nil || !validSecretSegment(segs[3]) {
			r.log.Warn("integrations: an operator value with an invalid kind, instance or field was ignored", "path", string(p), "by", v.By)
			continue
		}
		if out[ref] == nil {
			out[ref] = map[string]config.OperatorValue{}
		}
		out[ref][segs[3]] = v
	}
	return out
}

// slot is one effective instance and, once leased, its built instance. A
// slot is immutable but for its lifecycle fields; a change makes a new slot
// and retires the old one, which drains its leases and is then Closed
// (M1.4 step 4, M1.6).
type slot struct {
	cfg     InstanceConfig           // the effective view (Locked and Stored set)
	row     *StoredInstance          // nil for an operator-only instance
	ad      *adapter                 // nil when the adapter is not in this binary
	problem string                   // a Reason* when the instance cannot be built
	fp      string                   // fingerprint: equal fingerprints keep one slot
	opSec   map[string]secrets.Value // operator-sourced secret values by field
	// withheld lists the operator-sourced secret fields M1.4 step 2's
	// binding withholds (sorted); empty when every one is served.
	withheld []string
	// storedSecrets is whether the row's stored secrets may be served: the
	// row is stored and its destinations are those SecretsOrigin names.
	storedSecrets bool
	trust         Trust
	// given holds a Test's re-supplied secret values by field; nil for a
	// served instance.
	given map[string]secrets.Value

	state     atomic.Int64 // lease count, plus retiredBit once retired
	buildMu   sync.Mutex
	built     bool
	inst      Instance
	gate      Gate
	buildErr  error
	drained   chan struct{}
	drainOnce sync.Once
}

// retiredBit marks a retired slot in slot.state; the low bits count leases.
const retiredBit = int64(1) << 62

// effective builds ref's slot from its stored row (nil for none) and its
// operator values (nil for none). It does no I/O.
func (r *Registry) effective(ref Ref, row *StoredInstance, ops map[string]config.OperatorValue) *slot {
	s := &slot{drained: make(chan struct{})}
	var adapterID AdapterID
	switch {
	case row != nil:
		s.row = row
		s.cfg = cloneConfig(row.Config)
		s.cfg.Stored = true
		adapterID = row.Config.Adapter
	default:
		adapterID = r.o.OperatorInstances[ref]
		s.cfg = InstanceConfig{Ref: ref, Adapter: adapterID, Enabled: true}
	}
	s.cfg.Ref = ref
	s.cfg.Locked = nil
	ad := r.adapters[adapterID]
	switch {
	case row != nil && row.Malformed != "":
		s.problem = ReasonMalformed + ": " + row.Malformed
	case ad == nil:
		s.problem = ReasonAdapterNotInBinary
	case ad.desc.meta.Kind != ref.Kind:
		s.problem = ReasonInvalidSettings
	default:
		s.ad = ad
	}
	if s.cfg.Settings == nil {
		s.cfg.Settings = map[string]any{}
	}
	stored := maps.Clone(s.cfg.Settings)
	opSource := map[string]bool{}
	if len(ops) > 0 {
		s.cfg.Locked = make(map[string]string, len(ops))
		for _, f := range slices.Sorted(maps.Keys(ops)) {
			v := ops[f]
			s.cfg.Locked[f] = v.By
			if s.ad == nil {
				continue // not served: the instance is misconfigured
			}
			p, known := s.ad.fs.props[f]
			switch {
			case !known:
				r.log.Warn("integrations: an operator value names no settings field of the adapter and was ignored", "instance", ref.String(), "field", f, "by", v.By)
				delete(s.cfg.Locked, f)
			case p.secret:
				var str string
				if err := json.Unmarshal(v.Value, &str); err != nil || str == "" {
					r.log.Warn("integrations: an operator secret is not a non-empty JSON string and was ignored", "instance", ref.String(), "field", f, "by", v.By)
					delete(s.cfg.Locked, f)
					continue
				}
				if s.opSec == nil {
					s.opSec = map[string]secrets.Value{}
				}
				s.opSec[f] = secrets.NewValue(str)
				s.cfg.Settings[f] = secrets.RefTo(secretName(ref, f))
			default:
				val, err := coerce(p, v.Value)
				if err != nil {
					r.log.Warn("integrations: an operator value does not fit its settings field", "instance", ref.String(), "field", f, "by", v.By, "error", err)
					s.problem = ReasonInvalidSettings
					continue
				}
				s.cfg.Settings[f] = val
				opSource[f] = true
			}
		}
	}
	if s.ad != nil && s.problem == "" {
		if err := validateSettings(s.ad.desc.meta.Settings, s.ad.fs, s.cfg.Settings); err != nil {
			s.problem = ReasonInvalidSettings
		}
	}
	if s.ad != nil {
		r.bind(s, stored, opSource)
	}
	s.fp = fingerprint(s)
	return s
}

// bind applies the origin rules of M1.4 step 2 to s: which stored and which
// operator-sourced secrets may be served, and which hosts are trusted for
// private networks.
func (r *Registry) bind(s *slot, stored map[string]any, opSource map[string]bool) {
	fs := s.ad.fs
	keys := fs.destinationKeys()
	eff := fs.origins(s.cfg.Settings)
	storedOrigins := fs.origins(stored)
	// The operator origin of each destination key: the origin of the
	// operator's own value, or "" (the adapter default) when the operator
	// sets none for that key.
	opSettings := map[string]any{}
	for f := range opSource {
		opSettings[f] = s.cfg.Settings[f]
	}
	opOrigins := fs.origins(opSettings)
	// A destination is operator-sourced when every field that makes its
	// origin is: a URL field itself; for the host-and-port pair the host,
	// and the port unless no port is set at all.
	opDest := map[string]bool{}
	for _, k := range fs.urls {
		opDest[k] = opSource[k]
	}
	if fs.hostPort {
		_, portSet := s.cfg.Settings[portField]
		opDest[hostField] = opSource[hostField] && (opSource[portField] || !portSet)
	}

	// Stored secrets follow only the destination they were written for.
	if s.row != nil {
		s.storedSecrets = s.row.SecretsOrigin == "" || s.row.SecretsOrigin == signature(storedOrigins)
	}

	// Operator secrets bind to origin plus provenance (SEC7-3, R8-5).
	if len(s.opSec) > 0 {
		var oso map[string]string
		if s.row != nil {
			oso = s.row.OperatorSecretOrigin
		}
		served := true
		for _, k := range keys {
			e := eff[k]
			if e == opOrigins[k] {
				continue
			}
			if o, ok := oso[k]; ok && o != "" && o == e {
				continue
			}
			served = false
			break
		}
		if !served {
			s.withheld = slices.Sorted(maps.Keys(s.opSec))
		}
	}

	// Private-network trust follows the effective value's source: an
	// operator-sourced destination is trusted; a stored one only while Local
	// holds for the origin it was granted for.
	localHolds := s.row != nil && s.cfg.Local && s.row.LocalOrigin != "" && s.row.LocalOrigin == signature(storedOrigins)
	var trusted []string
	for _, k := range keys {
		o, ok := eff[k]
		if !ok {
			continue
		}
		if opDest[k] || (localHolds && storedOrigins[k] == o) {
			trusted = append(trusted, o)
		}
	}
	slices.Sort(trusted)
	s.trust = Trust{PrivateOrigins: slices.Compact(trusted)}
}

// fingerprint identifies a slot's effective state: the view, the trust, the
// withholding, the problem and the operator secrets (hashed, never kept).
func fingerprint(s *slot) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	view := s.cfg
	view.Updated = view.Updated.UTC()
	_ = enc.Encode(struct {
		Cfg      rowJSON
		Locked   map[string]string
		Stored   bool
		Problem  string
		Withheld []string
		Trust    Trust
		Secrets  bool
	}{
		Cfg: rowJSON{
			Kind: view.Ref.Kind, Instance: view.Ref.Instance, Adapter: view.Adapter, Name: view.Name,
			Enabled: view.Enabled, Priority: view.Priority, Origin: view.Origin, Local: view.Local,
			Settings: view.Settings, Limits: toLimitsJSON(view.Limits), Version: view.Version, Updated: view.Updated,
		},
		Locked: view.Locked, Stored: view.Stored, Problem: s.problem, Withheld: s.withheld, Trust: s.trust,
		Secrets: s.storedSecrets,
	})
	for _, f := range slices.Sorted(maps.Keys(s.opSec)) {
		sum := sha256.Sum256(s.opSec[f].Bytes())
		_, _ = h.Write([]byte(f))
		_, _ = h.Write(sum[:])
	}
	if s.row != nil {
		_, _ = fmt.Fprint(h, s.row.LocalOrigin, "|", s.row.SecretsOrigin, "|", signature(s.row.OperatorSecretOrigin))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cloneConfig is a deep enough copy of c for a caller to keep: the maps and
// the Limits pointer are copied.
func cloneConfig(c InstanceConfig) InstanceConfig {
	c.Settings = maps.Clone(c.Settings)
	c.Locked = maps.Clone(c.Locked)
	if c.Limits != nil {
		l := *c.Limits
		c.Limits = &l
	}
	return c
}

// secretReader is an instance's Env.Secrets: it reads only the secret
// fields the instance's settings declare, through the binding its slot
// computed (M1.4 step 2, M1.5).
type secretReader struct {
	r *Registry
	s *slot
}

// Secret resolves field at use time.
func (sr secretReader) Secret(ctx context.Context, field string) (secrets.Value, error) {
	return sr.r.secretFor(ctx, sr.s, field)
}

// secretFor resolves field of s: an operator value unless withheld, else
// the stored secret the row's ref names while the row's destinations are
// the ones it was written for. Any other case refuses before sending.
func (r *Registry) secretFor(ctx context.Context, s *slot, field string) (secrets.Value, error) {
	ref := s.cfg.Ref
	if v, ok := s.given[field]; ok {
		return secrets.NewValue(v.Reveal()), nil
	}
	if v, ok := s.opSec[field]; ok {
		if slices.Contains(s.withheld, field) {
			return secrets.Value{}, refused(ref, "secret", ErrOperatorSecretWithheld)
		}
		return secrets.NewValue(v.Reveal()), nil
	}
	if s.row == nil || !s.storedSecrets || s.ad == nil || !s.ad.fs.isSecret(field) {
		return secrets.Value{}, &Error{Ref: ref, Op: "secret", Class: ClassAuth, Err: fmt.Errorf("%w: secret field %q is not set", ErrNotConfigured, field)}
	}
	sref, ok := s.row.Config.Settings[field].(secrets.Ref)
	if !ok || sref.Name() != secretName(ref, field) {
		return secrets.Value{}, &Error{Ref: ref, Op: "secret", Class: ClassAuth, Err: fmt.Errorf("%w: secret field %q is not set", ErrNotConfigured, field)}
	}
	v, m, err := r.o.Secrets.Get(ctx, sref.Name())
	switch {
	case errors.Is(err, secrets.ErrNotSet):
		return secrets.Value{}, &Error{Ref: ref, Op: "secret", Class: ClassAuth, Err: fmt.Errorf("%w: secret field %q is not set", ErrNotConfigured, field)}
	case err != nil:
		return secrets.Value{}, &Error{Ref: ref, Op: "secret", Class: ClassAuth, Err: fmt.Errorf("secret field %q is unreadable: %w", field, err)}
	case m.Source != secrets.SourceStored:
		// An operator value core secrets holds under the name is not the
		// stored copy: operator secrets reach an instance through the
		// overlay and its origin binding only.
		return secrets.Value{}, refused(ref, "secret", ErrOperatorSecretWithheld)
	}
	return v, nil
}
