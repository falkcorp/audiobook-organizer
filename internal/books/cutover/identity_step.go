// file: internal/books/cutover/identity_step.go
// version: 1.0.0
// guid: abba1ac6-9d86-49a6-aebf-666fa71faa65
// last-edited: 2026-11-12

package cutover

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// IdentityStep is M3, Identity (03 §3.8 row M3; F4-06): HEAD's users, roles,
// API keys, OAuth identities and invite allow-list into core's auth and rbac
// stores through their importers, with the stop-the-upgrade preflight, the
// operator's fix and delete, and Verify. The mapping, the decoders and the
// step live in internal/books/cutover/m3; this assembles the declaration the
// kernel runs.
//
// raisesLevel and after are the flip's: F3's books level and the StepRef of
// books' level step (BooksLevelStepRef), the same two M2 takes. cfg.StepID is
// IdentityCutoverID, and may be left empty.
//
// Nothing registers it: F4-08 adds it to the cutover's step list, beside the
// importers and the catalog only the booted core has. The step is a cutover
// step (Breaking, BootBlocking) over the identity families' retired claims
// and the idx: sub-claims beneath them (m3.SourceRanges).
func IdentityStep(cfg m3.Config, raisesLevel int, after storage.StepRef) (datamigrate.DataMigration, error) {
	switch {
	case raisesLevel <= 0:
		return datamigrate.DataMigration{}, errors.New("cutover: IdentityStep: raisesLevel is the flip's books level, above 0")
	case after.Owner != BooksOwner || after.Version <= 0:
		return datamigrate.DataMigration{}, errors.New("cutover: IdentityStep: after is books' level step (BooksLevelStepRef)")
	case cfg.StepID != "" && cfg.StepID != IdentityCutoverID:
		return datamigrate.DataMigration{}, fmt.Errorf("cutover: IdentityStep: Config.StepID is %s, not another ID", IdentityCutoverID)
	}
	cfg.StepID = IdentityCutoverID
	m, err := m3.New(cfg)
	if err != nil {
		return datamigrate.DataMigration{}, fmt.Errorf("cutover: IdentityStep: %w", err)
	}
	dm := datamigrate.DataMigration{
		ID:          IdentityCutoverID,
		Owner:       BooksOwner,
		Description: "users, roles, API keys, OAuth identities and the invite allow-list into core's auth and rbac stores",
		Source:      m3.SourceRanges(),
		// Sequential, on purpose: a key follows its owner and an identity its
		// user, in one chunk's batch, and no row may be imported twice, so the
		// order is the invariant. The families hold tens to hundreds of rows.
		Parallelism:     tasks.Sequential("tens to hundreds of identity rows; a key follows its owner and no row is imported twice"),
		Mode:            datamigrate.BootBlocking,
		Compat:          storage.Breaking,
		RaisesLevel:     raisesLevel,
		RewritesInPlace: true,
		After:           []storage.StepRef{after},
		Cutover:         true,
		Sensitive:       m3.SensitivePrefixes(),
		Step:            m.Step,
		Verify:          m.Verify,
	}
	wirePreflight(&dm, m)
	return dm, nil
}
