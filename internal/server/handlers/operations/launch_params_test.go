// file: internal/server/handlers/operations/launch_params_test.go
// version: 1.0.0
// guid: 4b91d70e-6c25-4a83-8f14-2e705c9a6db3
// last-edited: 2026-08-11

package operations_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The bug this pins was live in production and invisible.
//
// launchOp receives the raw request body as []byte and hands it to
// EnqueueOp(ctx, defID, params any), which json.Marshal's params. Marshalling a
// []byte BASE64-ENCODES it, so a body of
//
//	{"book_ids":["b1"],"fetch_metadata_first":true}
//
// was stored as the JSON string "eyJib29rX2lkcyI6...". Every op decoding those
// params into its struct then hit
//
//	json: cannot unmarshal string into Go value of type server.libraryOrganizeParams
//
// and — before wave 3 — discarded that error, leaving the params struct at its
// zero value. So POST /operations/organize with an explicit book_ids list ran
// with BookIDs nil, which downstream means "no selection given" = the WHOLE
// LIBRARY. Every filter and flag sent through /operations/{scan,organize,
// transcode} was dropped, silently, for as long as this has been in place.
//
// The tell was there all along: library.transcode already decoded its params
// strictly, so POST /operations/transcode has been failing outright rather than
// silently — one endpoint loudly broken while its two siblings quietly did the
// wrong thing, all from the same line.
//
// This test deliberately asserts on encoding/json's behaviour rather than
// through the handler: the defect is entirely in which TYPE is handed to
// Marshal, and a handler-level test would pass just as happily on the broken
// version as long as it never inspected the enqueued bytes.

func TestRawBodyMustEnqueueAsRawMessage_NotBase64(t *testing.T) {
	body := []byte(`{"book_ids":["b1","b2"],"fetch_metadata_first":true}`)

	// What the code used to do.
	broken, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal []byte: %v", err)
	}
	// What it does now.
	fixed, err := json.Marshal(json.RawMessage(body))
	if err != nil {
		t.Fatalf("marshal RawMessage: %v", err)
	}

	if string(fixed) != string(body) {
		t.Fatalf("json.RawMessage must round-trip verbatim.\n got: %s\nwant: %s", fixed, body)
	}
	if string(broken) == string(body) {
		t.Fatal("marshalling a bare []byte no longer base64-encodes it — this test's premise " +
			"has changed and the comment above is now wrong; re-verify before trusting it")
	}

	// The consequence, spelled out: the broken form does not decode back into a
	// params struct at all, and the error names the string type.
	var p struct {
		BookIDs            []string `json:"book_ids"`
		FetchMetadataFirst bool     `json:"fetch_metadata_first"`
	}
	if err := json.Unmarshal(broken, &p); err == nil {
		t.Fatal("expected the base64 form to fail decoding into the params struct")
	}
	if err := json.Unmarshal(fixed, &p); err != nil {
		t.Fatalf("the fixed form must decode cleanly: %v", err)
	}
	if len(p.BookIDs) != 2 || !p.FetchMetadataFirst {
		t.Fatalf("params lost in transit: %+v", p)
	}
}

// TestStartOrganize_EnqueuesDecodableParams is the assertion the existing
// coverage was missing.
//
// TestStartOrganize_Enqueues already exercised this exact endpoint — but it
// matched the params argument with mock.Anything, so it asserted only that an
// enqueue HAPPENED, never what was enqueued. It passed for the entire life of
// the base64 defect. A mock argument matcher is a place a bug can hide in plain
// sight: the looser the matcher, the less the test knows.
//
// Here the params are captured and decoded exactly as the op will decode them.
func TestStartOrganize_EnqueuesDecodableParams(t *testing.T) {
	h, _, reg, _, _, _ := newTestHandler(t)

	var captured any
	reg.EXPECT().EnqueueOp(mock.Anything, "library.organize", mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
			captured = params
			return "op-organize", nil
		})

	const reqBody = `{"book_ids":["b1","b2"],"fetch_metadata_first":true}`
	w := run(http.MethodPost, "/operations/organize", "/operations/organize", []byte(reqBody), func(r *gin.Engine) {
		r.POST("/operations/organize", h.StartOrganize)
	})
	require.Equal(t, http.StatusAccepted, w.Code)
	require.NotNil(t, captured, "handler did not enqueue anything")

	// Reproduce what the registry does to params on the way to the op.
	marshalled, err := json.Marshal(captured)
	require.NoError(t, err)

	var p struct {
		BookIDs            []string `json:"book_ids"`
		FetchMetadataFirst bool     `json:"fetch_metadata_first"`
	}
	require.NoError(t, json.Unmarshal(marshalled, &p),
		"enqueued params did not survive the registry's json.Marshal — got %s", marshalled)

	// The scope field is the one that matters: nil BookIDs is not "no books",
	// it is "no selection" = the whole library.
	assert.Equal(t, []string{"b1", "b2"}, p.BookIDs,
		"the caller's book selection must reach the op; an empty list here means a full-library organize")
	assert.True(t, p.FetchMetadataFirst, "flags must survive enqueue too")
}
