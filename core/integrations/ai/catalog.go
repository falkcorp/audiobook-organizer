// file: core/integrations/ai/catalog.go
// version: 1.0.0
// guid: 2f6a9c40-1b87-4d35-a0e2-6c3d8f5b9e21
// last-edited: 2026-11-12

package ai

import (
	"fmt"
	"iter"
	"slices"
	"strings"
)

// The capability catalog is the Router's: modules register their capabilities
// through Registrar.AICapability (F7-09), which calls RegisterCapability with
// the module's ID, and the kernel freezes the catalog before Run. There is no
// package-level registry and no runtime registration: a capability exists only
// if a module's code registers it, so a handle of one Router means nothing to
// another.

// catalogEntry is one registered capability.
type catalogEntry struct {
	spec      CapabilitySpec
	module    string
	applyKind string // set by BatchApplyTask; "" = no apply task
}

// RegisterCapability adds spec to the catalog on behalf of module and returns
// its handle; the zero handle and an error that wraps ErrInvalidCapability when
// spec is refused: an ID that is not "<module>.<name>" with the registering
// module's ID as its prefix, a duplicate, an unknown kind or feature, a
// required feature the kind cannot have, or a missing Description, DataSent or
// TypicalRequest. A call after FreezeCatalog is ErrCatalogFrozen.
func (r *Router) RegisterCapability(module string, spec CapabilitySpec) (Capability, error) {
	if err := validateSpec(module, spec); err != nil {
		return Capability{}, err
	}
	if spec.AttemptTimeout <= 0 {
		spec.AttemptTimeout = defaultAttemptTimeout(spec.Kind)
	}
	spec.RequiredFeatures = slices.Clone(spec.RequiredFeatures)
	spec.OptionalFeatures = slices.Clone(spec.OptionalFeatures)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return Capability{}, fmt.Errorf("%w: %s", ErrCatalogFrozen, spec.ID)
	}
	if _, dup := r.catalog[spec.ID]; dup {
		return Capability{}, fmt.Errorf("%w: %s is already registered", ErrInvalidCapability, spec.ID)
	}
	r.catalog[spec.ID] = &catalogEntry{spec: spec, module: module}
	return Capability{id: spec.ID, r: r}, nil
}

// FreezeCatalog closes the catalog: no capability is added after it, and the
// kernel calls it before Run. It is idempotent.
func (r *Router) FreezeCatalog() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Catalog yields the registered specs in ID order.
func (r *Router) Catalog() iter.Seq[CapabilitySpec] {
	r.mu.RLock()
	specs := make([]CapabilitySpec, 0, len(r.catalog))
	for _, e := range r.catalog {
		s := e.spec
		s.RequiredFeatures = slices.Clone(s.RequiredFeatures)
		s.OptionalFeatures = slices.Clone(s.OptionalFeatures)
		specs = append(specs, s)
	}
	r.mu.RUnlock()
	slices.SortFunc(specs, func(a, b CapabilitySpec) int { return strings.Compare(a.ID, b.ID) })
	return slices.Values(specs)
}

// Lookup resolves a catalog ID to its handle and spec; catalog IDs only: a
// stored tick for an ID the catalog does not know (a capability of a disabled
// module, or of a binary without it) resolves to nothing here and is kept,
// ignored, by the row that holds it.
func (r *Router) Lookup(id string) (Capability, CapabilitySpec, bool) {
	e := r.entry(id)
	if e == nil {
		return Capability{}, CapabilitySpec{}, false
	}
	return Capability{id: id, r: r}, e.spec, true
}

// HasCapability reports whether id is a catalog ID (CatalogView).
func (r *Router) HasCapability(id string) bool { return r.entry(id) != nil }

// HasApplyKind reports whether kind is the apply kind of a registered
// BatchApplyTask (CatalogView).
func (r *Router) HasApplyKind(kind string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.catalog {
		if e.applyKind != "" && e.applyKind == kind {
			return true
		}
	}
	return false
}

// entry is the catalog entry of id, or nil.
func (r *Router) entry(id string) *catalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.catalog[id]
}

// resolve returns c's entry; ErrUnknownCapability for a handle that is zero,
// another Router's, or not in this catalog.
func (r *Router) resolve(c Capability) (*catalogEntry, error) {
	if c.r != r || c.id == "" {
		return nil, fmt.Errorf("%w: %q", ErrUnknownCapability, c.id)
	}
	e := r.entry(c.id)
	if e == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownCapability, c.id)
	}
	return e, nil
}

// validateSpec applies the registration rules.
func validateSpec(module string, s CapabilitySpec) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrInvalidCapability, s.ID, fmt.Sprintf(format, args...))
	}
	if module == "" {
		return bad("no registering module")
	}
	prefix, name, ok := strings.Cut(s.ID, ".")
	if !ok || prefix != module {
		return bad("the ID must be %q followed by a name, not another module's prefix", module+".<name>")
	}
	if !validCapabilityName(name) {
		return bad("the name %q does not match [a-z][a-z0-9_]{0,63}", name)
	}
	if !s.Kind.Valid() {
		return bad("kind %q is not text, embed or transcribe", string(s.Kind))
	}
	if s.AttemptTimeout < 0 {
		return bad("a negative attempt timeout")
	}
	if s.Locality > LocalOnly {
		return bad("unknown locality floor %d", s.Locality)
	}
	for _, f := range slices.Concat(s.RequiredFeatures, s.OptionalFeatures) {
		if !validFeature(f) {
			return bad("unknown feature %q", string(f))
		}
	}
	for _, f := range s.RequiredFeatures {
		if !featureFitsKind(f, s.Kind) {
			return bad("feature %q cannot be required of %s work", string(f), s.Kind)
		}
	}
	for field, text := range map[string]string{"Description": s.Description, "DataSent": s.DataSent, "TypicalRequest": s.TypicalRequest} {
		if strings.TrimSpace(text) == "" {
			return bad("%s is required", field)
		}
	}
	return nil
}

func validCapabilityName(n string) bool {
	if n == "" || len(n) > 64 || n[0] < 'a' || n[0] > 'z' {
		return false
	}
	for i := 1; i < len(n); i++ {
		c := n[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func validFeature(f Feature) bool {
	switch f {
	case FeatureJSONSchema, FeatureJSONMode, FeatureVision, FeatureBatch, FeatureTools, FeatureTimestamps:
		return true
	}
	rest, ok := strings.CutPrefix(string(f), string(FeatureSharedFS))
	return ok && rest != ""
}

// featureFitsKind says which kind a required feature can be asked of.
func featureFitsKind(f Feature, k Kind) bool {
	switch f {
	case FeatureJSONSchema, FeatureJSONMode, FeatureVision, FeatureBatch, FeatureTools:
		return k == KindText
	case FeatureTimestamps:
		return k == KindTranscribe
	}
	return k == KindTranscribe // shared_fs:<root>
}
