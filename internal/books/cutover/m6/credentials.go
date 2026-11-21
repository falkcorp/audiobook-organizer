// file: internal/books/cutover/m6/credentials.go
// version: 1.0.0
// guid: 6c1d9e47-3b52-4a08-8f6d-2e7a5b90c413

package m6

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The credentials of a metadata source, and what the books adapters can hold.
//
// M2 moves a source's credentials map out of the blob into the secrets store
// under integrations.metadata.<id>.credentials.<snake k> ("apiKey" becomes
// credentials.api_key), and M6 binds every name under the instance's prefix as a
// setting of the same dotted name. A books metadata adapter declares only the
// secret fields its HEAD client reads: google-books api_key and hardcover
// api_token, nothing on the other four. core's settings schema is closed
// (additionalProperties false) and a secret field name cannot contain a dot, so an
// instance holding a credentials.<k> setting is HealthMisconfigured
// (integration_settings_invalid), served by no chain, and sends no request: the
// whole source goes dark, not only the credential. This file folds the credential
// HEAD actually sent into the declared field and drops (and quarantines and
// reports) every other secret setting, so the row the adapter loads is healthy.

// metadataSecretField is one secret field a books metadata adapter declares and
// the lookup order HEAD used for it (internal/metafetch/service_search.go):
// google-books read the operator's key, then credentials["apiKey"]; hardcover
// read the operator's token, then credentials["api_token"], then
// credentials["apiKey"]. A name is the settings key a credential sits under:
// the declared field itself is the operator's, a credentials.<k> key is the map's.
type metadataSecretField struct {
	field string
	order []string
}

// metadataAdapters is the six sources the books metadata adapters serve, by
// instance ID, with the secret field each declares (none for four of them). An
// ID that is not here has no adapter in this binary and its instance is imported
// as it is (the Registry keeps and reports a row whose adapter it does not
// register). Pinned against the adapters' descriptors by
// TestMetadataCredentials_DeclaredFieldsMatchTheAdapters.
var metadataAdapters = map[string]*metadataSecretField{
	"audible":      nil,
	"audnexus":     nil,
	"google-books": {field: "api_key", order: []string{"api_key", "credentials.api_key"}},
	"hardcover":    {field: "api_token", order: []string{"api_token", "credentials.api_token", "credentials.api_key"}},
	"openlibrary":  nil,
	"wikipedia":    nil,
}

// droppedRef is a secret setting the plan leaves unbound.
type droppedRef struct {
	field string
	name  secrets.Name
	// folded: the secret is bound under the declared field instead (a carry), so
	// it is neither quarantined nor reported as lost.
	folded bool
	// shadowedBy is the field whose secret HEAD used instead, for the report.
	shadowedBy string
}

// foldPlan is what to do with the secret settings of one metadata instance.
type foldPlan struct {
	// carry binds a credentials.<k> secret under the declared field: the value
	// is copied to the declared field's name (cs.to) and the setting is that ref.
	carry []carriedSecret
	// drop is every secret setting that ends up unbound, folded ones included.
	drop []droppedRef
}

// planCredentialFold plans the secret settings of metadata/<id>. have is the
// secret settings the instance would hold, by settings key, with the name of the
// secret each refers to (for a row being repaired and an exact-ID source this is
// what the row or refsFor holds; for a renamed source it is the credentials M2
// stored under HEAD's ID, still under their old names, by the field they carry).
// ok is false for an ID with no adapter, which is left alone.
//
// A renamed source (HEAD's ID was not the instance ID) was an inert unknown
// source at HEAD (its provider switch was exact and case-sensitive), so HEAD sent
// it no credential and none is folded into a declared field: every
// credentials.<k> is dropped. An exact-ID source folds by HEAD's order.
func planCredentialFold(id string, renamed bool, ref integrations.Ref, have map[string]secrets.Name) (plan foldPlan, ok bool) {
	decl, known := metadataAdapters[id]
	if !known {
		return foldPlan{}, false
	}
	declared := ""
	if decl != nil {
		declared = decl.field
	}
	winner := ""
	if decl != nil && !renamed {
		for _, k := range decl.order {
			if _, present := have[k]; present {
				winner = k
				break
			}
		}
	}
	for _, field := range slices.Sorted(keysOf(have)) {
		name := have[field]
		switch {
		case field == winner && field == declared:
			// The declared field's own secret wins: bound as it is.
		case field == winner:
			plan.carry = append(plan.carry, carriedSecret{from: name, to: secretName(ref, declared), field: declared, folded: true})
			plan.drop = append(plan.drop, droppedRef{field: field, name: name, folded: true})
		default:
			d := droppedRef{field: field, name: name}
			if winner != "" {
				d.shadowedBy = winner
			}
			plan.drop = append(plan.drop, d)
		}
	}
	return plan, true
}

func keysOf[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// dropLine is the report line of a dropped secret setting, without the value.
func (d droppedRef) line() string {
	switch {
	case d.shadowedBy != "":
		return "secret setting " + d.field + " is not bound: HEAD used the " + d.shadowedBy + " secret of this source first and the adapter declares no field for it (its secret " + string(d.name) + " stays in the store, unreferenced)"
	default:
		return "secret setting " + d.field + " is not bound: HEAD sent this source no such credential and its adapter declares no field for it (its secret " + string(d.name) + " stays in the store, unreferenced)"
	}
}

// foldMetadataCredentials applies planCredentialFold to p, whose settings are the
// refs secretRefs gave it (an exact-ID source) or whose carry holds the
// credentials of a renamed one. The settings it drops are quarantined by secret
// name under the member (a name, never a value) and reported with the instance.
func (p *pending) foldMetadataCredentials(id string, renamed bool, ref integrations.Ref, settings map[string]any) {
	have := map[string]secrets.Name{}
	if renamed {
		for _, cs := range p.carry {
			have[cs.field] = cs.from
		}
	} else {
		for k, v := range settings {
			if r, ok := v.(secrets.Ref); ok {
				have[k] = r.Name()
			}
		}
	}
	plan, ok := planCredentialFold(id, renamed, ref, have)
	if !ok {
		return
	}
	for _, d := range plan.drop {
		if renamed {
			p.carry = slices.DeleteFunc(p.carry, func(cs carriedSecret) bool { return cs.field == d.field })
		} else {
			delete(settings, d.field)
		}
		if d.folded {
			continue
		}
		p.dropped = append(p.dropped, d.line())
		p.droppedValues = append(p.droppedValues, droppedValue{path: "secrets." + d.field, value: string(d.name), reason: reasonUnreadCredential})
	}
	p.carry = append(p.carry, plan.carry...)
}

// RepairConfig is what RepairMetadataCredentials needs: the instance store M6
// wrote and the secrets it reads and writes. The names mirror Config's.
type RepairConfig struct {
	// Store is core's instance store over its keyspace.
	Store integrations.InstanceStore
	// Secrets stages the carried secret.
	Secrets secrets.Importer
	// NewBatch opens the batch one row's repair is staged into and committed in.
	NewBatch func() *storage.Batch
	// SecretValue is as in Config: a secret's value, found false when it is
	// unreadable.
	SecretValue func(ctx context.Context, n secrets.Name) (value string, found bool, err error)
	// Audit stages one core.integration.update record per repaired row.
	Audit integrations.AuditRecorder
	// Report receives one entry per repaired row and per secret setting dropped.
	// nil keeps none.
	Report *Report
	// By names the actor of a carried secret.
	By string
}

// RepairResult counts what RepairMetadataCredentials did.
type RepairResult struct {
	// Repaired is the rows rewritten, Unchanged the metadata rows it read that
	// needed nothing, Skipped the rows it left alone (no adapter in this binary,
	// not a migration row, or malformed).
	Repaired, Unchanged, Skipped int
}

// RepairMetadataCredentials folds or drops the dotted credentials.<k> settings of
// the metadata instances an M6 that predates the fold already wrote. M6's import
// is create-only (a row that differs is an ErrImportConflict, quarantined), so
// running M6 again cannot fix them: this is the step for a store M6 already ran
// on (a prod-clone rehearsal, or any build that took the old M6 through its
// cutover). It rewrites only the migration-origin rows of the six HEAD sources,
// by the same plan M6 applies to a fresh one (planCredentialFold), one batch per
// row, and is idempotent: a repaired row holds no secret setting its adapter
// does not declare, so a second run finds nothing. It never deletes a secret.
//
// It cannot tell an exact-ID source from one renamed onto a source ID (the row
// records neither), so it plans every row as an exact-ID one; a row renamed onto
// one of the six names is vanishingly rare, and its credential is its own, bound
// only to the destinations the row's secrets origin records. Run it with the
// Registry not yet loaded: it writes the store, not the Registry.
func RepairMetadataCredentials(ctx context.Context, cfg RepairConfig) (RepairResult, error) {
	var res RepairResult
	switch {
	case cfg.Store == nil || cfg.Secrets == nil || cfg.NewBatch == nil || cfg.Audit == nil:
		return res, errors.New("m6: RepairConfig.Store, Secrets, NewBatch and Audit are required")
	case cfg.SecretValue == nil:
		return res, errors.New("m6: RepairConfig.SecretValue is required")
	}
	rows, err := cfg.Store.Load(ctx)
	if err != nil {
		return res, fmt.Errorf("m6: load the instances to repair: %w", err)
	}
	by := cfg.By
	if by == "" {
		by = "m6"
	}
	for _, row := range rows {
		c := row.Config
		id := string(c.Ref.Instance)
		if c.Ref.Kind != integrations.KindMetadata || row.Malformed != "" || c.Origin != integrations.OriginMigration {
			res.Skipped++
			continue
		}
		if _, known := metadataAdapters[id]; !known {
			res.Skipped++
			continue
		}
		have := map[string]secrets.Name{}
		for k, v := range c.Settings {
			if r, ok := v.(secrets.Ref); ok {
				have[k] = r.Name()
			}
		}
		plan, _ := planCredentialFold(id, false, c.Ref, have)
		if len(plan.drop) == 0 {
			res.Unchanged++
			continue
		}
		if err := repairRow(ctx, cfg, by, row, plan); err != nil {
			return res, err
		}
		res.Repaired++
	}
	return res, nil
}

// repairRow rewrites one row by plan: the settings it drops removed, the carried
// secret copied under the declared field's name and bound.
func repairRow(ctx context.Context, cfg RepairConfig, by string, row integrations.StoredInstance, plan foldPlan) error {
	ref := row.Config.Ref
	settings := make(map[string]any, len(row.Config.Settings))
	maps.Copy(settings, row.Config.Settings)
	for _, d := range plan.drop {
		delete(settings, d.field)
	}
	type moved struct {
		cs         carriedSecret
		val        string
		unreadable bool
	}
	var mv []moved
	for _, cs := range plan.carry {
		val, found, err := cfg.SecretValue(ctx, cs.from)
		if err != nil {
			return fmt.Errorf("m6: read the credential %s of %s: %w", cs.field, ref, err)
		}
		settings[cs.field] = secrets.RefTo(cs.to)
		mv = append(mv, moved{cs: cs, val: val, unreadable: !found})
	}
	row.Config.Settings = settings
	b := cfg.NewBatch()
	if _, err := cfg.Store.Put(ctx, b, row); err != nil {
		return fmt.Errorf("m6: rewrite %s: %w", ref, err)
	}
	for _, m := range mv {
		var err error
		if m.unreadable {
			err = cfg.Secrets.ImportUnreadable(ctx, b, m.cs.to, unreadableCarried, by)
		} else {
			err = cfg.Secrets.ImportSecret(ctx, b, secrets.Write{Name: m.cs.to, Value: secrets.NewValue(m.val), By: by})
		}
		if err != nil && !errors.Is(err, storage.ErrImportConflict) {
			return fmt.Errorf("m6: carry a credential to %s: %w", m.cs.to, err)
		}
	}
	var dropped []string
	for _, d := range plan.drop {
		dropped = append(dropped, d.field)
	}
	if err := cfg.Audit.RecordIn(ctx, b, auditUpdate(ref, dropped)); err != nil {
		return fmt.Errorf("m6: audit the repair of %s: %w", ref, err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("m6: commit the repair of %s: %w", ref, err)
	}
	cfg.report(Entry{Kind: "instance", ID: ref.String(), Detail: "repaired: " + strings.Join(dropped, ", ") + " settings folded or dropped"})
	for _, m := range mv {
		cfg.report(Entry{Kind: "note", ID: ref.String(), Detail: "the credential " + string(m.cs.from) + " is folded into " + m.cs.field + " (HEAD read it as this source's key)"})
	}
	for _, d := range plan.drop {
		if !d.folded {
			cfg.report(Entry{Kind: "note", ID: ref.String(), Detail: d.line()})
		}
	}
	return nil
}

func (c RepairConfig) report(e Entry) {
	if c.Report == nil {
		return
	}
	c.Report.mu.Lock()
	defer c.Report.mu.Unlock()
	c.Report.entries = append(c.Report.entries, e)
}

// auditUpdate is the record of one repaired row: the same target core's own
// records for an instance use, the names of the settings left unbound and never a
// value.
func auditUpdate(ref integrations.Ref, fields []string) audit.Event {
	return audit.Event{
		Action: "core.integration.update", Outcome: audit.Success,
		Target:  entity.Ref{Module: "core", Kind: "integration", ID: ref.String()},
		Details: map[string]any{"instance": ref.String(), "origin": string(integrations.OriginMigration), "unbound_settings": fields, "by": "m6 repair"},
	}
}
