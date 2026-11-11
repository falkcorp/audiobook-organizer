// file: core/storage/datastep.go
// version: 1.0.2
// guid: bce49c80-c743-47cf-91bc-803ce9df2291
// last-edited: 2026-11-10

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/storage/codec"
)

// DataStep is one BootBlocking data migration as the boot's migration DAG
// sees it (05-storage §5S.3.5 "Order", §5S.3.6; 06 §6.5.1 P4b). The
// migration itself is a storage/datamigrate declaration and its runner is
// core/internal/storagemigrate; storage, which imports neither (L1, SIMP-17),
// only orders the step among the versioned steps, counts it in the
// checkpoint decision (§5S.4 step 6), records it done in a fresh install's
// one batch, and calls Run at its place in the DAG, before step 9, so a
// failed step leaves the attempt's checkpoint open. The kernel builds the
// list (OpenOptions.DataSteps). Name coined by F2-06.
//
// A step is pending while its record c/storage/dm/<ID> (DataMigrationKey)
// is not done. Its edges: every After; its owner's versioned steps below
// Barrier (all of them when Barrier is 0), so it runs after them; and, when
// Barrier is set, the owner's versioned step Barrier runs after it (so every
// later version waits for it, §5S.3.6 "Barrier versions stay blocked").
// Barrier must name one of the owner's registered versions (a SpecError at
// Open otherwise), and a pending step whose Barrier version the store has
// already applied fails the boot with ErrBarrierApplied: the version it was
// to hold back ran before it. A
// core owner's step also runs after the highest books legacy step, as a core
// versioned step does. Ties fall to the owner order of §5S.3.5, then the
// owner's versioned steps before its data steps, data steps in the order
// declared.
type DataStep struct {
	Owner   Owner
	ID      string    // the data migration ID, "<owner>.<name>"
	After   []StepRef // explicit edges (a versioned step or another data step)
	Barrier int       // the owner's version that may not run before this step is done; 0 = none
	// Cutover marks an R2 cutover step: it runs whether its owner is enabled
	// or not (06 A5); any other step of a disabled owner is deferred.
	Cutover bool
	// Rewrite marks M8, the store rewrite (§5S.3.13): a boot whose plan
	// holds it pending needs the store's size once more in free space, for
	// the copy beside the store (§5S.3.12 "Free space"). F3-04.
	Rewrite bool
	// Run runs the step to completion on db, the store the boot opened, and
	// records its own state under DataMigrationKey(ID). An error halts the
	// DAG with ErrMigrationFailed, the attempt's checkpoint open; Run wraps
	// its step's own failure (not its bookkeeping, not a cancelled boot) in
	// a *StepError, as the versioned runner does (§5S.3.5 "StepError").
	Run func(ctx context.Context, db *DB) error

	order int // declaration index, the tie-break among one owner's data steps
}

// ErrBarrierApplied: a pending data step's Barrier version is already
// applied on this store, so the version the step was to hold back already
// ran (a release that added the step with a Barrier at or below a shipped
// version). The boot fails before any step runs. Name coined by F2-06.
var ErrBarrierApplied = errors.New("storage: a data step's Barrier version is already applied")

// ref is the step's StepRef.
func (s *DataStep) ref() StepRef { return StepRef{Owner: s.Owner, DataMigration: s.ID} }

// DataMigrationKey is the key, relative to storage's own keyspace c/storage/
// (the owner "core/storage"), of data migration id's state record
// c/storage/dm/<id> (05-storage §5S.3.6). The runner keeps the migration's
// plan, shard cursors, held items and report beneath it: each is this key
// with more keys.Join segments appended. Name coined by F2-06.
func DataMigrationKey(id string) []byte { return dmKey(id) }

// DataMigrationRecord is a data migration's state record, the value of
// DataMigrationKey(id): State is one of datamigrate's states, and the DAG
// treats the step as done exactly when it is DataMigrationDone. Name coined
// by F2-06.
type DataMigrationRecord struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
	Build string    `json:"build"`
}

// DataMigrationDone is the state that completes a data migration; it is
// never re-entered (DATA2-15).
const DataMigrationDone = "done"

// DataMigrationCodec is the state record's codec: the one storage decodes
// when it plans the DAG, so the runner encodes with it too.
func DataMigrationCodec() codec.Codec[DataMigrationRecord] {
	return codec.JSON[DataMigrationRecord](1)
}

// attachDataSteps validates the build's data steps and hangs each on its
// owner, then checks the DAG's shape with them in it (§5S.4 step 4: every
// problem listed). Open calls it with every boot, data steps or none.
func attachDataSteps(owners map[Owner]*ownerInfo, steps []DataStep) error {
	steps = slices.Clone(steps)
	var probs []string
	seen := map[string]bool{}
	for i := range steps {
		s := &steps[i]
		s.order = i
		name := fmt.Sprintf("data step %d (%s)", i, s.ID)
		o := owners[s.Owner]
		switch {
		case s.ID == "":
			probs = append(probs, name+": empty ID")
			continue
		case seen[s.ID]:
			probs = append(probs, name+": ID declared twice")
			continue
		case o == nil:
			probs = append(probs, fmt.Sprintf("%s: owner %q is not a registered storage owner", name, s.Owner))
			continue
		case s.Run == nil:
			probs = append(probs, name+": no Run")
		case s.Barrier < 0:
			probs = append(probs, fmt.Sprintf("%s: Barrier %d is negative", name, s.Barrier))
		case s.Barrier > 0 && !slices.ContainsFunc(o.spec.Migrations, func(m Migration) bool { return m.Version == s.Barrier }):
			probs = append(probs, fmt.Sprintf("%s: Barrier %d names no registered version of %s", name, s.Barrier, s.Owner))
		}
		for _, a := range s.After {
			if (a.Version > 0) == (a.DataMigration != "") || a.Version < 0 || a.Owner == "" {
				probs = append(probs, fmt.Sprintf("%s: After %s names neither one version nor one data migration of an owner", name, a))
			}
		}
		seen[s.ID] = true
		o.data = append(o.data, s)
	}
	if len(probs) > 0 {
		return &SpecError{Problems: probs}
	}
	return checkDAGShape(owners)
}
