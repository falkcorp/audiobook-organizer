// file: internal/fingerprint/workerclient/contract_test.go
// version: 1.0.0
// guid: aa2ae629-e32e-4b32-83ad-2746bfbfde91
// last-edited: 2026-09-19

package workerclient

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/fpworker"
)

// runChangedHub answers every run-scoped call with ErrRunChanged, wrapped
// the way the real hub may wrap it.
type runChangedHub struct{}

func (runChangedHub) Hello(context.Context, workerapi.HelloRequest) (*workerapi.HelloResponse, error) {
	return &workerapi.HelloResponse{RunID: "run-2"}, nil
}
func (runChangedHub) Lease(context.Context, workerapi.LeaseRequest) (*workerapi.LeaseResponse, error) {
	return nil, workerapi.ErrRunChanged
}
func (runChangedHub) Renew(string, workerapi.RenewRequest) (*workerapi.RenewResponse, error) {
	return nil, workerapi.ErrRunChanged
}
func (runChangedHub) Release(string, workerapi.ReleaseRequest) (*workerapi.ReleaseResponse, error) {
	return &workerapi.ReleaseResponse{}, nil
}
func (runChangedHub) Results(context.Context, workerapi.ResultsRequest) (*workerapi.ResultsResponse, error) {
	return nil, workerapi.ErrRunChanged
}

// TestContract_RunChangedThroughTheRealHandler drives the real HTTP handler
// with the worker's own API client: what the server sends for ErrRunChanged
// must be exactly what isRunChanged recognizes on every run-scoped call, and
// what a worker that predates run IDs treats as fatal.
func TestContract_RunChangedThroughTheRealHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	fpworker.New(runChangedHub{}, func() bool { return true }).Register(r.Group("/api/v1"))
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	base, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	api := &apiClient{base: base, key: testKey, hc: ts.Client()}
	ctx := context.Background()

	_, st, err := api.lease(ctx, workerapi.LeaseRequest{WorkerID: "w1", RunID: "run-1"})
	if !isRunChanged(st, err) {
		t.Errorf("lease: status %d err %v is not recognized as a run change", st, err)
	}
	if fatalStatus(st, err) == nil {
		t.Errorf("lease: a worker that predates run IDs must treat status %d as fatal", st)
	}
	st, err = api.renew(ctx, "L1", workerapi.RenewRequest{WorkerID: "w1", RunID: "run-1"})
	if !isRunChanged(st, err) {
		t.Errorf("renew: status %d err %v is not recognized as a run change", st, err)
	}
	_, st, err = api.results(ctx, workerapi.ResultsRequest{WorkerID: "w1", RunID: "run-1", Results: []workerapi.JobResult{}})
	if !isRunChanged(st, err) {
		t.Errorf("results: status %d err %v is not recognized as a run change", st, err)
	}
}
