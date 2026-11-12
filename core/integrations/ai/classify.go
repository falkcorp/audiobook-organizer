// file: core/integrations/ai/classify.go
// version: 1.0.0
// guid: 3c8e5a17-9d24-4f60-b1a7-5e0f2c9d8b36
// last-edited: 2026-11-12

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/falkcorp/mediacore/integrations"
)

// quotaMarkers are the provider vocabulary of an exhausted allowance. Both the
// "type" and the "code" field of an error body are checked against every
// marker: the captured production payload sets them to DIFFERENT values
// ("insufficient_quota" / "credit_balance_exhausted"), and reading one field
// is what broke HEAD's classifier before.
func quotaMarkers() []string {
	return []string{"insufficient_quota", "credit_balance_exhausted"}
}

// IsQuotaMarker reports whether an error body's type or code names an
// exhausted quota. The adapter and the router share it, so a 429 is Quota in
// both or in neither.
func IsQuotaMarker(typ, code string) bool {
	for _, m := range quotaMarkers() {
		if typ == m || code == m {
			return true
		}
	}
	return false
}

// QuotaMarkerIn reports whether detail, an error body excerpt as an adapter
// keeps it in integrations.Error.Detail, carries a quota marker in its type or
// code field. Detail that is not JSON is searched for the marker words, so a
// scrubbed or truncated body still classifies.
func QuotaMarkerIn(detail string) bool {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return false
	}
	var body struct {
		Type  string `json:"type"`
		Code  string `json:"code"`
		Error *struct {
			Type string `json:"type"`
			Code any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(detail), &body); err == nil {
		if IsQuotaMarker(body.Type, body.Code) {
			return true
		}
		if e := body.Error; e != nil {
			code, _ := e.Code.(string)
			return IsQuotaMarker(e.Type, code)
		}
		return false
	}
	low := strings.ToLower(detail)
	for _, m := range quotaMarkers() {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// StatusClass is the failover table's reading of an HTTP status plus the
// provider error body's type and code: what an adapter that builds
// *integrations.Error from a provider response uses. A 429 with a quota marker
// in type OR code is ClassQuota; a plain 429 is ClassRateLimited.
func StatusClass(status int, typ, code string) integrations.Class {
	switch {
	case status == 429:
		if IsQuotaMarker(typ, code) {
			return integrations.ClassQuota
		}
		return integrations.ClassRateLimited
	case status == 401 || status == 403:
		return integrations.ClassAuth
	case status == 404:
		return integrations.ClassNotFound
	case status == 408 || status >= 500:
		return integrations.ClassTransient
	case status >= 400:
		return integrations.ClassInvalid
	}
	return integrations.ClassUnknown
}

// outcome is what the router does with one attempt's result.
type outcome struct {
	class    integrations.Class
	deadline bool // the per-attempt deadline expired while the caller's ctx was live
}

// failsOver says whether an attempt of this class may be retried on another
// endpoint (the 05-ai section 5 table).
//
//   - Transient, RateLimited, Auth, Quota and Unsupported: the endpoint is the
//     problem, another may serve; they bench it.
//   - RefusedLocal: we refused before sending (egress guard, a disabled
//     instance, an operator secret withheld); another endpoint may serve and
//     this one is not benched, because nothing was learned about it.
//   - BadOutput, Invalid, NotFound and Unknown: the endpoint answered and the
//     answer (or an error nobody recognises) is the problem; another endpoint
//     would reject it too, or hide it, or double the load. Returned as is.
//   - Canceled: the caller's own budget; stop.
func failsOver(c integrations.Class) bool {
	switch c {
	case integrations.ClassTransient, integrations.ClassRateLimited, integrations.ClassAuth,
		integrations.ClassQuota, integrations.ClassUnsupported, integrations.ClassRefusedLocal:
		return true
	}
	return false
}

// benches says whether an attempt of class c marks the endpoint unhealthy.
func benches(c integrations.Class) bool {
	switch c {
	case integrations.ClassTransient, integrations.ClassRateLimited, integrations.ClassAuth,
		integrations.ClassQuota, integrations.ClassUnsupported:
		return true
	}
	return false
}

// classify decides what an attempt's error means. parent is the CALLER's
// context, not the per-attempt one: a done parent means stop, while a deadline
// on a live parent means the per-attempt timeout fired (the deadline flag,
// which the loop computes from the attempt context itself, because
// integrations.ClassOf cannot tell the two apart for an error it did not
// build).
//
// The order is load-bearing: a done parent wins over everything; an explicit
// quality mark beats inference; the deadline beats whatever shape the SDK gave
// the timeout; an adapter's own *integrations.Error class stands, except that
// a rate limit whose body carries a quota marker is a Quota; everything else
// goes through integrations.ClassOf, whose Unknown does not fail over.
func classify(parent context.Context, err error, deadline bool) outcome {
	if err == nil {
		return outcome{}
	}
	if parent != nil && parent.Err() != nil {
		return outcome{class: integrations.ClassCanceled}
	}
	if _, ok := errors.AsType[*QualityError](err); ok {
		return outcome{class: integrations.ClassBadOutput}
	}
	if deadline {
		return outcome{class: integrations.ClassTransient, deadline: true}
	}
	class := integrations.ClassOf(parent, err)
	if e, ok := errors.AsType[*integrations.Error](err); ok {
		if (class == integrations.ClassRateLimited || e.Status == 429) && QuotaMarkerIn(e.Detail) {
			class = integrations.ClassQuota
		}
	}
	return outcome{class: class}
}
