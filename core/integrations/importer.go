// file: core/integrations/importer.go
// version: 1.0.0
// guid: 4d4c4598-17e3-44ff-83be-ad14e5b9b4d9
// last-edited: 2026-11-11

package integrations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// Importer is what an app's cutover step calls to write its pre-core
// instances as native rows (R2 step M6, M1.10). The kernel builds it over
// c/integrations/ and hands it to the step in module.Importers (06 §6.4.2;
// 05-storage §5S.3.13 states the importer rules). ImportInstance
// validates the record as Put does (ID grammar, settings schema, secret
// fields holding secrets.Ref only), sets Origin "migration", takes Local,
// LocalOrigin and OperatorSecretOrigin as the step gives them (the step runs
// as the app's system actor, not as an interactive principal), and stages the
// row and its audit record into b, so the row commits with the step's
// deletion of the rows it came from. A record whose adapter this binary does
// not register is imported and kept, as M1.4 step 2 keeps one; a record that
// fails schema validation is imported with Malformed set and reported,
// rather than refused into the quarantine, because the Registry already
// serves such a row as misconfigured. It writes no secret: the secrets the refs name were written by the
// config-and-secrets step before it (05-config).
//
// What the native importer can see (F7-01): NativeImporter takes a keyspace
// only, so it holds no adapter schema. It validates what a row alone shows,
// and refuses (ErrInvalid, nothing staged) a record that could not be a row
// or would bind a secret it does not own: the ref and instance ID grammar,
// the adapter ID grammar, every secrets.Ref in Settings named
// integrations.<kind>.<instance>.<its field>, no secret value in Settings,
// and well-formed trust origins. The schema judgement, "adapter not in this
// binary" and a settings mismatch, is the Registry's at Load, which serves
// such a row as HealthMisconfigured: that is the "kept and reported" path.
// The import is create-only: an identical row is already current (a re-run
// stages nothing, the audit record included), any other row fails with
// storage.ErrImportConflict.
type Importer interface {
	ImportInstance(ctx context.Context, b *storage.Batch, inst StoredInstance) error
}

// AuditRecorder stages one audit record into the caller's batch:
// audit.Logger's RecordIn, the only audit method the Registry and the
// importer call (name coined by F7-01).
type AuditRecorder interface {
	RecordIn(ctx context.Context, b *storage.Batch, e audit.Event) error
}

// ImporterOption configures NativeImporter (name coined by F7-01).
type ImporterOption func(*nativeImporter)

// ImportAudit is the audit store the importer stages its records with:
// core.integration.create for every row and core.integration.local_migrated
// for a row granted Local (05-rbac §7.1, M1.10). The kernel passes it;
// without it ImportInstance refuses, so no import grants trust unaudited.
func ImportAudit(a AuditRecorder) ImporterOption {
	return func(im *nativeImporter) { im.audit = a }
}

// NativeImporter is the importer over c/integrations/. The options are a
// deviation from M1.3's NativeImporter(ks), additive at every call site: the
// audit record the importer stages needs an audit store a keyspace does not
// carry.
func NativeImporter(ks storage.Keyspace, opts ...ImporterOption) Importer {
	im := &nativeImporter{s: instancestore.New(ks, rowVersion)}
	for _, o := range opts {
		o(im)
	}
	return im
}

// nativeImporter is NativeImporter's Importer.
type nativeImporter struct {
	s     *instancestore.Store
	audit AuditRecorder
}

// ImportInstance validates inst, forces Origin migration and version 1, and
// stages the row and its audit records into b.
func (im *nativeImporter) ImportInstance(ctx context.Context, b *storage.Batch, inst StoredInstance) error {
	if b == nil {
		return errors.New("integrations: ImportInstance needs the caller's batch")
	}
	if im.audit == nil {
		return errors.New("integrations: the importer has no audit store (ImportAudit); an import is never unaudited")
	}
	if err := checkImport(inst); err != nil {
		return err
	}
	inst.Config.Origin = OriginMigration
	inst.Config.Version = 0
	inst.Config.Locked, inst.Config.Stored = nil, false
	inst.Malformed = ""
	if !inst.Config.Local {
		inst.LocalOrigin = ""
	}
	val, err := encodeRow(inst, 1)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	ref := inst.Config.Ref
	staged, err := im.s.Import(ctx, b, string(ref.Kind), string(ref.Instance), val)
	if err != nil || !staged {
		return err
	}
	details := map[string]any{"instance": ref.String(), "adapter": string(inst.Config.Adapter), "origin": string(OriginMigration)}
	if err := im.audit.RecordIn(ctx, b, audit.Event{
		Action: "core.integration.create", Outcome: audit.Success, Target: instanceEntity(ref), Details: details,
	}); err != nil {
		return fmt.Errorf("integrations: audit the import of %s: %w", ref, err)
	}
	if inst.Config.Local {
		if err := im.audit.RecordIn(ctx, b, audit.Event{
			Action: "core.integration.local_migrated", Outcome: audit.Success, Target: instanceEntity(ref),
			Details: map[string]any{"instance": ref.String(), "local_origin": inst.LocalOrigin},
		}); err != nil {
			return fmt.Errorf("integrations: audit the Local grant of %s: %w", ref, err)
		}
	}
	return nil
}

// checkImport is the importer's structural validation (see Importer).
func checkImport(inst StoredInstance) error {
	c := inst.Config
	if err := c.Ref.Validate(); err != nil {
		return err
	}
	if err := c.Adapter.Validate(); err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(c.Settings)) {
		switch v := c.Settings[k].(type) {
		case secrets.Ref:
			if v.Name() != secretName(c.Ref, k) {
				return invalidf("settings field %q of %s names the secret %q; an instance's secret field names only %q", k, c.Ref, v.Name(), secretName(c.Ref, k))
			}
		case *secrets.Ref:
			return invalidf("settings field %q of %s holds a *secrets.Ref; a secret field holds a secrets.Ref", k, c.Ref)
		case secrets.Value, *secrets.Value:
			return invalidf("settings field %q of %s holds a secret value; a row holds secrets.Refs only", k, c.Ref)
		default:
			if name, ok := refName(v); ok && name != secretName(c.Ref, k) {
				return invalidf("settings field %q of %s names the secret %q; an instance's secret field names only %q", k, c.Ref, name, secretName(c.Ref, k))
			}
		}
	}
	if c.Local && inst.LocalOrigin == "" {
		return invalidf("%s is imported with Local but no LocalOrigin: Local is bound to the origin it was granted for", c.Ref)
	}
	for _, o := range append([]string{inst.LocalOrigin}, slices.Collect(maps.Values(inst.OperatorSecretOrigin))...) {
		if o != "" && !wellFormedOrigin(o) {
			return invalidf("%s: trust origin %q is not scheme://host[:port] or key=origin pairs", c.Ref, o)
		}
	}
	return nil
}

// wellFormedOrigin reports whether o is an origin (or a signature of
// key=origin pairs) as the Registry renders them.
func wellFormedOrigin(o string) bool {
	for part := range strings.FieldsSeq(o) {
		_, origin, ok := strings.Cut(part, "=")
		if !ok {
			origin = part
		}
		if originOf(origin) != origin {
			return false
		}
	}
	return true
}
