// file: internal/server/op_params_decode_test.go
// version: 1.0.0
// guid: 5a7c1e93-2d84-4f60-b1a7-9e3c05d8f271
// last-edited: 2026-08-11

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Wave 3 of the silent-failure sweep: every v2 operation decoded its params with
// `_ = json.Unmarshal(rawParams, &p)`. A malformed or wrongly-typed params blob
// left the struct at its zero value and the op ran anyway.
//
// These tests call the REAL registered OperationDef — pulled back out of the
// registry by ID, not a hand-copied closure — so they cannot drift from what the
// server actually registers.
//
// Why a zero-value &Server{} is enough: in all ten ops below the params decode is
// the FIRST statement in the Run closure, so it returns before anything touches
// the Server, the store, or the reporter. That property is the whole reason the
// fix is safe to apply uniformly — aborting at the decode discards no work — and
// asserting it here means a future edit that moves real work above the decode
// breaks these tests with a nil-pointer panic rather than passing quietly.

// w3decodeReg builds a registry backed by a mock store. RegisterOp persists the
// definition row (upsertDefToDB), so the store cannot be nil — but that write is
// the only store call any of these tests provoke, because every Run under test
// returns at its params decode.
func w3decodeReg(t *testing.T) *opsregistry.Registry {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	return opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
}

// w3registrar names one op and the method that registers it.
type w3registrar struct {
	opID     string
	register func(*Server, *opsregistry.Registry) error
	// fixed marks the ops Wave 3 changed. The two ops marked false were already
	// correct on main and are included as consistency controls: if the fix is
	// right, the ops it touched behave exactly like the ones that never needed it.
	fixed bool
}

func w3allOps() []w3registrar {
	return []w3registrar{
		// --- fixed by Wave 3 (the eight internal/server/* sites) ---
		{"library.scan", (*Server).RegisterLibraryScanOp, true},
		{"library.organize", (*Server).RegisterLibraryOrganizeOp, true},
		{"library.folder-auto-scan", (*Server).RegisterFolderAutoScanOp, true},
		{"itunes.path-reconcile", (*Server).RegisterITunesPathReconcileOp, true},
		{"itunes.path-repair", (*Server).RegisterITunesPathRepairOp, true},
		{"openlibrary.download", (*Server).RegisterOLDownloadOp, true},
		{"openlibrary.import", (*Server).RegisterOLImportOp, true},
		{"diagnostics.export", (*Server).RegisterDiagnosticsExportOp, true},

		// --- already correct on main; the audit calls library.transcode the
		// in-repo template the fix was modelled on ---
		{"library.transcode", (*Server).RegisterLibraryTranscodeOp, false},
		{"library.import", (*Server).RegisterLibraryImportOp, false},
	}
}

// w3runOpWithParams registers one op and invokes its real Run with the given
// body.
//
// It reports whether the call panicked, and that is a load-bearing signal rather
// than a nuisance. On a zero-value Server an op that gets PAST its params decode
// walks into real work (scanner.PerformScan, the iTunes service, …) and nil-derefs
// almost immediately. So:
//
//	returns an error, no panic  → stopped at/near the decode
//	panics                      → ran past the decode into real work
//
// which is exactly the distinction these tests need to draw, and it is why the
// empty-params case below cannot simply assert err == nil.
func w3runOpWithParams(t *testing.T, r w3registrar, body string) (err error, panicked bool) {
	t.Helper()
	reg := w3decodeReg(t)
	s := &Server{}
	require.NoError(t, r.register(s, reg), "registering %s", r.opID)

	def, ok := reg.Def(r.opID)
	require.True(t, ok, "op %s was not registered under that ID", r.opID)
	require.NotNil(t, def.Run, "op %s has no Run", r.opID)

	defer func() {
		if rec := recover(); rec != nil {
			panicked = true
			err = nil
		}
	}()
	return def.Run(context.Background(), json.RawMessage(body), nil), false
}

// TestOpParams_MalformedJSONIsRefused is the core assertion: a params blob that
// is not valid JSON must fail the op rather than run it with zero values.
func TestOpParams_MalformedJSONIsRefused(t *testing.T) {
	// Truncated object — a SyntaxError for every params struct regardless of
	// which fields it declares.
	const malformed = `{"folder_path":`

	for _, r := range w3allOps() {
		t.Run(r.opID, func(t *testing.T) {
			err, panicked := w3runOpWithParams(t, r, malformed)
			require.Falsef(t, panicked,
				"%s ran PAST the params decode on malformed JSON and reached real work", r.opID)
			require.Error(t, err, "%s accepted malformed params and ran anyway", r.opID)
			require.Truef(t,
				strings.Contains(err.Error(), "decode params") ||
					strings.Contains(err.Error(), "parse import params"),
				"%s failed, but not at the params decode: %v", r.opID, err)
		})
	}
}

// TestLibraryOrganize_ScopeTypeErrorIsRefused is the case with teeth, and the
// reason library.organize is the highest-priority site in the wave.
//
// book_ids is the SCOPE of the organize run. Sent as a string instead of an
// array — an ordinary client bug — json.Unmarshal returns an UnmarshalTypeError
// and leaves BookIDs nil. With the error discarded, nil BookIDs does not mean
// "organize nothing"; downstream it means "no explicit selection", i.e. organize
// the WHOLE LIBRARY. A request to organize one book silently became a
// full-library file-moving run.
func TestLibraryOrganize_ScopeTypeErrorIsRefused(t *testing.T) {
	r := w3registrar{"library.organize", (*Server).RegisterLibraryOrganizeOp, true}

	err, panicked := w3runOpWithParams(t, r, `{"book_ids":"01HX9Q0000000000000000000"}`)
	require.False(t, panicked, "library.organize ran past the decode and began organizing")
	require.Error(t, err, "a string book_ids must be refused, not silently widened to the whole library")
	require.Contains(t, err.Error(), "decode params")

	// Pin the mechanism, not just the outcome: confirm this payload really is a
	// type error that leaves the scope field empty. If encoding/json ever
	// tolerated it, the test above would be asserting the wrong thing.
	var p libraryOrganizeParams
	decodeErr := json.Unmarshal([]byte(`{"book_ids":"01HX9Q0000000000000000000"}`), &p)
	require.Error(t, decodeErr)
	require.Empty(t, p.BookIDs, "the discarded-error path really did leave the scope unset")
}

// TestOpParams_EmptyParamsStillAccepted guards the other direction. Every one of
// these ops is legitimately enqueueable with no params at all, and the fix must
// not turn that into a failure — the decode is skipped entirely when the blob is
// empty. Without this, the wave could "pass" by refusing everything.
func TestOpParams_EmptyParamsStillAccepted(t *testing.T) {
	for _, r := range w3allOps() {
		t.Run(r.opID, func(t *testing.T) {
			err, panicked := w3runOpWithParams(t, r, ``)
			if panicked || err == nil {
				return // reached real work — i.e. got past the decode, which is the point
			}
			require.NotContains(t, err.Error(), "decode params",
				"%s rejected EMPTY params at the decode; empty must skip decoding entirely", r.opID)
			require.NotContains(t, err.Error(), "parse import params",
				"%s rejected EMPTY params at the decode; empty must skip decoding entirely", r.opID)
		})
	}
}
