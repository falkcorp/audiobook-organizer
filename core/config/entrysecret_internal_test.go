// file: core/config/entrysecret_internal_test.go
// version: 1.0.0
// guid: cc0dc185-4d79-4e87-a128-e03a22a5e8aa
// last-edited: 2026-11-10

package config

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

type flare struct {
	ID     string      `json:"id"`
	Issuer string      `json:"issuer"`
	Key    secrets.Ref `json:"key" protect:"admin"`
}

type flares struct {
	Flares []flare `json:"flares" protect:"owner"`
}

// Validate quotes the issuer it refuses.
func (f *flares) Validate() error {
	for _, e := range f.Flares {
		if strings.HasPrefix(e.Issuer, "joker") {
			return errors.New("issuer " + e.Issuer + " is not trusted")
		}
	}
	return nil
}

// rowsOnly is an OverrideStore that loads fixed rows.
type rowsOnly []Override

func (r rowsOnly) Load(context.Context) iter.Seq2[Override, error] {
	return func(yield func(Override, error) bool) {
		for _, ov := range r {
			if !yield(ov, nil) {
				return
			}
		}
	}
}

func (rowsOnly) Stage(context.Context, *storage.Batch, []Override, []Path) error { return nil }

// TestEntrySecret_HealthDetailRedacted: a stored list that fails its
// section's Validator is quarantined, and the health detail config raises
// for it carries the redacted error, never the Validator's text.
func TestEntrySecret_HealthDetailRedacted(t *testing.T) {
	const leak = "jokers-wild-hunter2"
	s, err := NewStore(Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := Register(s.Registry(), Define("core.gotham.flares", flares{})); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	row := Override{Path: "core.gotham.flares.flares", Value: json.RawMessage(`[{"id":"batcave","issuer":"joker ` + leak + `"}]`), By: "alfred"}
	rep, err := s.Attach(ctx, rowsOnly{row}, nopCommitter{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quarantined[row.Path] == nil {
		t.Fatalf("report %+v", rep)
	}
	detail, ok := s.health[healthOverridesQuarantined]
	if !ok {
		t.Fatalf("health %v", s.health)
	}
	if strings.Contains(detail, leak) || strings.Contains(rep.Quarantined[row.Path].Error(), leak) {
		t.Errorf("health detail %q, report %v", detail, rep.Quarantined[row.Path])
	}
}

type nopCommitter struct{}

func (nopCommitter) Commit(context.Context, Commit) error { return nil }
