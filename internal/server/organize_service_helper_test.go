// file: internal/server/organize_service_helper_test.go
// version: 1.0.0
// guid: 3e7b1c52-6a0d-4f94-8b2e-c5d1a9f40e68
// last-edited: 2026-11-28

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// organizeTestStore is the store a test hands to newOrganizeServiceForTest: a
// database.Store that also has organizer.RelocatingCopyCreator, which is on the
// production *database.PebbleStore and not on database.Store (it may not grow:
// TestStoreFlattenedWidth). Requiring it in the signature makes the compiler
// refuse a store that would leave the single-book organize copy unwired.
type organizeTestStore interface {
	database.Store
	organizer.RelocatingCopyCreator
}

// newOrganizeServiceForTest builds the organize service the way production
// does: through the audiobooks package's own ServiceDef for KeyOrganize, over
// the given store in the "store" slot. It is the only way a test outside
// package audiobooks gets one, because the package's constructor is
// unexported and has no production caller outside that def.
func newOrganizeServiceForTest(t testing.TB, store organizeTestStore) *OrganizeService {
	t.Helper()
	var defs []serviceregistry.ServiceDef
	for _, def := range audiobooks.ServiceDefs() {
		if def.Name == serviceregistry.KeyOrganize {
			defs = append(defs, def)
		}
	}
	require.Len(t, defs, 1, "audiobooks.ServiceDefs must define %q exactly once", serviceregistry.KeyOrganize)
	c := serviceregistry.NewContainerFrom(defs...).Override(serviceregistry.KeyStore, store)
	require.NoError(t, c.Resolve())
	require.NoError(t, c.Build(t.Context()))
	return serviceregistry.Get[*OrganizeService](c, serviceregistry.KeyOrganize)
}
