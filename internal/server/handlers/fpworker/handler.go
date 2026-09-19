// file: internal/server/handlers/fpworker/handler.go
// version: 1.1.0
// guid: 561e8ac1-7cd0-46f4-8b06-e0461af0be94
// last-edited: 2026-09-19

// Package fpworker serves the remote fingerprint worker API
// (/api/v1/fingerprint/worker/*): fp-worker processes on other hosts lease
// windowed-fingerprint jobs from a running acoustid.window-backfill and post
// their results. Design: .claude/notes/windowed-fingerprint-design-2026-09-12.md
// section (d).
//
// Status codes: 404 while fingerprint_remote_workers_enabled is off (checked
// per request, ahead of authentication, so a disabled API does not even
// reveal that it exists); 503 when no live window-backfill is running; 204
// from lease when nothing is leaseable; 409 for a pipeline or tool build that
// is not allowlisted; 410 for an expired or reclaimed lease; 429 for a worker
// over its lease cap; 413 for a body over 8 MB.
package fpworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// Hub is the lease manager (acoustid.WorkerHub).
type Hub interface {
	Hello(ctx context.Context) (*workerapi.HelloResponse, error)
	Lease(ctx context.Context, req workerapi.LeaseRequest) (*workerapi.LeaseResponse, error)
	Renew(leaseID string, req workerapi.RenewRequest) (*workerapi.RenewResponse, error)
	Release(leaseID string, req workerapi.ReleaseRequest) (*workerapi.ReleaseResponse, error)
	Results(ctx context.Context, req workerapi.ResultsRequest) (*workerapi.ResultsResponse, error)
}

// Handler serves the worker routes.
type Handler struct {
	hub     Hub
	enabled func() bool
}

// New builds the handler. hub may be nil (the acoustid plugin is not
// registered): every route then answers 503. enabled is read per request.
func New(hub Hub, enabled func() bool) *Handler {
	return &Handler{hub: hub, enabled: enabled}
}

// Register mounts the routes on g. guards run after the flag check and before
// every handler: the rate limiter, authentication and the permission check.
func (h *Handler) Register(g *gin.RouterGroup, guards ...gin.HandlerFunc) {
	chain := append([]gin.HandlerFunc{h.flagGuard, bodyCap}, guards...)
	w := g.Group("/fingerprint/worker", chain...)
	w.GET("/hello", h.hello)
	w.POST("/lease", h.lease)
	w.POST("/lease/:id/renew", h.renew)
	w.POST("/lease/:id/release", h.release)
	w.POST("/results", h.results)
}

// flagGuard answers 404 while the feature flag is off.
func (h *Handler) flagGuard(c *gin.Context) {
	if h.enabled == nil || !h.enabled() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Next()
}

// bodyCap bounds every body at workerapi.MaxBodyBytes: a declared length over
// it is refused before any read, and a chunked body is cut off by
// MaxBytesReader and mapped to 413 in decode.
func bodyCap(c *gin.Context) {
	if c.Request.ContentLength > workerapi.MaxBodyBytes {
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "REQUEST_TOO_LARGE")
		c.Abort()
		return
	}
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workerapi.MaxBodyBytes)
	}
	c.Next()
}

// decode reads a JSON body into v; false means a response was written. An
// empty body is accepted only when allowEmpty is set.
func decode(c *gin.Context, v any, allowEmpty bool) bool {
	err := io.EOF
	if c.Request.Body != nil {
		err = json.NewDecoder(c.Request.Body).Decode(v)
	}
	if err == nil {
		return true
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "REQUEST_TOO_LARGE")
		return false
	}
	if errors.Is(err, io.EOF) {
		if allowEmpty {
			return true
		}
		httputil.RespondWithBadRequest(c, "request body required")
		return false
	}
	httputil.RespondWithBadRequest(c, "invalid JSON body")
	return false
}

// respondErr maps the hub's errors to status codes. Every message is a
// sentinel's fixed text except 400, whose detail the hub builds from limits
// (never from request values); the API key is never part of any message.
func (h *Handler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, workerapi.ErrNoRun):
		httputil.RespondWithServiceUnavailable(c, workerapi.ErrNoRun.Error())
	case errors.Is(err, workerapi.ErrLeaseGone):
		httputil.RespondWithError(c, http.StatusGone, workerapi.ErrLeaseGone.Error(), "LEASE_GONE")
	case errors.Is(err, workerapi.ErrToolsNotAllowed):
		httputil.RespondWithError(c, http.StatusConflict, workerapi.ErrToolsNotAllowed.Error(), "TOOLS_NOT_ALLOWED")
	case errors.Is(err, workerapi.ErrTooManyLeases):
		httputil.RespondWithError(c, http.StatusTooManyRequests, workerapi.ErrTooManyLeases.Error(), "TOO_MANY_LEASES")
	case errors.Is(err, workerapi.ErrBadRequest):
		httputil.RespondWithBadRequest(c, err.Error())
	default:
		httputil.RespondWithInternalError(c, "fingerprint worker request failed")
	}
}

func (h *Handler) available(c *gin.Context) bool {
	if h.hub == nil {
		httputil.RespondWithServiceUnavailable(c, workerapi.ErrNoRun.Error())
		return false
	}
	return true
}

func (h *Handler) hello(c *gin.Context) {
	if !h.available(c) {
		return
	}
	resp, err := h.hub.Hello(c.Request.Context())
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) lease(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var req workerapi.LeaseRequest
	if !decode(c, &req, false) {
		return
	}
	resp, err := h.hub.Lease(c.Request.Context(), req)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	if resp == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) renew(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var req workerapi.RenewRequest
	if !decode(c, &req, false) {
		return
	}
	resp, err := h.hub.Renew(c.Param("id"), req)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) release(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var req workerapi.ReleaseRequest
	if !decode(c, &req, true) {
		return
	}
	resp, err := h.hub.Release(c.Param("id"), req)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) results(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var req workerapi.ResultsRequest
	if !decode(c, &req, false) {
		return
	}
	resp, err := h.hub.Results(c.Request.Context(), req)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
