// file: core/kernel/cutover_native.go
// version: 0.1.0
// guid: 4d9e2b71-6a05-4f3c-8c1e-b7a3d5f09e62
// last-edited: 2026-11-22

package kernel

import (
	"fmt"

	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// logOwnerSpec is the journal's storage owner (core/log, c/log/): a plain
// keyspace with no migrations and no level (logjournal.StorageSpec). It is
// claimed in every app's store, as core/tasks is: the keyspace is storage's to
// register, and an app that never writes a journal row leaves it empty.
func logOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: logjournal.Owner, Spec: logjournal.StorageSpec(), Enabled: true}
}

// bindNativeImporters points Importers.Tasks and Importers.Log at the native
// task store and the journal over db's c/tasks/ and c/log/ (F5-18; 06 §6.5.1
// P4b: "the kernel binds each importer to its subsystem's keyspace before the
// DAG runs"). Both are built over the keyspace and nothing else, so the P4c
// task stores and the P7 journal that later open the same keyspaces share no
// state with them beyond the rows. It writes nothing.
func bindNativeImporters(l *lateImporters, db *storage.DB) error {
	tks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		return fmt.Errorf("kernel: tasks importer: no %s keyspace in the store", taskstore.Owner)
	}
	lks, ok := db.Keyspace(logjournal.Owner)
	if !ok {
		return fmt.Errorf("kernel: log importer: no %s keyspace in the store", logjournal.Owner)
	}
	l.bindNative(tasks.NativeImporter(tks), tasks.NativeStores(tks).Runs, logjournal.NewImporter(logjournal.New(lks)))
	return nil
}
