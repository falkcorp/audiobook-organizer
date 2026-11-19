// file: internal/books/appconfig/residual.go
// version: 1.0.0
// guid: c8d3a1f6-5e72-4b09-9a4d-0f1b6e7c2d85
// last-edited: 2026-11-19

package appconfig

import (
	"context"
	"encoding/json"
)

// Residual is where the class-i fields live: the integrations settings M2 leaves
// in the legacy blob until F7 (root_dir, metadata_sources, ai_endpoints,
// download_client, plugins ...). The books module implements it with the blob's
// frozen codec, which also moves the secrets inside those values to and from
// the secret store; the Engine treats a value as opaque JSON of the Config field
// the row names. An Engine built without WithResidual leaves those fields zero
// and refuses a write to them.
type Residual interface {
	// Load is every stored class-i field by HEAD name, secrets included in
	// plaintext (the services need them). A field never stored is absent.
	Load(ctx context.Context) (map[string]json.RawMessage, error)
	// Save writes the given fields and removes the unset ones in one commit.
	Save(ctx context.Context, set map[string]json.RawMessage, unset []string) error
	// Redact is the form of a field's value a response may carry: nested secrets
	// removed. head is HEAD's name and v the field's JSON.
	Redact(head string, v json.RawMessage) json.RawMessage
}

// Option configures New.
type Option func(*Engine)

// WithResidual gives the Engine the class-i fields' home.
func WithResidual(r Residual) Option { return func(e *Engine) { e.res = r } }
