// file: internal/httputil/respond.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-08-14

// Package httputil provides shared HTTP response helpers for all packages
// that handle gin HTTP requests (server, middleware, itunes/service, etc).
package httputil

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// RespondWithError sends a standardized error response and logs it.
func RespondWithError(c *gin.Context, statusCode int, message string, code string) {
	logErrorWithContext(c, statusCode, message)
	c.JSON(statusCode, ErrorResponse{
		Error:  message,
		Code:   code,
		Status: statusCode,
	})
}

// RespondWithBadRequest sends a 400 Bad Request error response.
func RespondWithBadRequest(c *gin.Context, message string) {
	RespondWithError(c, http.StatusBadRequest, message, "BAD_REQUEST")
}

// RespondWithValidationError sends a 400 error for validation failures.
func RespondWithValidationError(c *gin.Context, field string, reason string) {
	message := "validation error: " + field
	if reason != "" {
		message = message + " (" + reason + ")"
	}
	RespondWithError(c, http.StatusBadRequest, message, "VALIDATION_ERROR")
}

// RespondWithNotFound sends a 404 Not Found error response.
func RespondWithNotFound(c *gin.Context, resourceType string, id string) {
	message := resourceType + " not found"
	if id != "" {
		message = message + ": " + id
	}
	RespondWithError(c, http.StatusNotFound, message, "NOT_FOUND")
}

// RespondWithInternalError sends a 500 Internal Server Error response.
func RespondWithInternalError(c *gin.Context, message string) {
	RespondWithError(c, http.StatusInternalServerError, message, "INTERNAL_ERROR")
}

// RespondWithConflict sends a 409 Conflict error response.
func RespondWithConflict(c *gin.Context, message string) {
	RespondWithError(c, http.StatusConflict, message, "CONFLICT")
}

// RespondWithUnauthorized sends a 401 Unauthorized error response.
func RespondWithUnauthorized(c *gin.Context, message string) {
	RespondWithError(c, http.StatusUnauthorized, message, "UNAUTHORIZED")
}

// RespondWithForbidden sends a 403 Forbidden error response.
func RespondWithForbidden(c *gin.Context, message string) {
	RespondWithError(c, http.StatusForbidden, message, "FORBIDDEN")
}

// RespondWithServiceUnavailable sends a 503 Service Unavailable error response.
func RespondWithServiceUnavailable(c *gin.Context, message string) {
	RespondWithError(c, http.StatusServiceUnavailable, message, "SERVICE_UNAVAILABLE")
}

// InternalError logs the full underlying error then sends a 500 response.
// Use this when you have a concrete error value to log but only want to expose
// a generic message to the client.
func InternalError(c *gin.Context, msg string, err error) {
	// Error text routinely wraps user-controlled strings (file names, tag
	// values) — escape CR/LF so it cannot forge log records (CA12).
	slog.Error(msg, "err", logging.SanitizeErr(err))
	RespondWithInternalError(c, msg)
}

// RespondWithSuccess sends a successful response with data.
func RespondWithSuccess(c *gin.Context, statusCode int, data any) {
	c.JSON(statusCode, SuccessResponse{Data: data})
}

// RespondWithOK sends a 200 OK response.
func RespondWithOK(c *gin.Context, data any) {
	RespondWithSuccess(c, http.StatusOK, data)
}

// RespondWithCreated sends a 201 Created response.
func RespondWithCreated(c *gin.Context, data any) {
	RespondWithSuccess(c, http.StatusCreated, data)
}

// RespondWithNoContent sends a 204 No Content response.
func RespondWithNoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// RespondWithList sends a 200 OK paginated list response.
func RespondWithList(c *gin.Context, items any, count int, limit int, offset int) {
	c.JSON(http.StatusOK, ListResponse{
		Items:  items,
		Count:  count,
		Limit:  limit,
		Offset: offset,
		Total:  count,
	})
}

func logErrorWithContext(c *gin.Context, statusCode int, message string) {
	method := c.Request.Method
	// Path and message derive from the request; escape CR/LF so a crafted
	// URL cannot forge log records (CA12).
	path := logging.Sanitize(c.Request.URL.Path)
	message = logging.Sanitize(message)
	clientIP := c.ClientIP()
	if statusCode >= 500 {
		slog.Error("http request", "method", method, "path", path, "status", statusCode, "message", message, "clientIP", clientIP)
	} else {
		slog.Warn("http request", "method", method, "path", path, "status", statusCode, "message", message, "clientIP", clientIP)
	}
}
