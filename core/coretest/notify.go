// file: core/coretest/notify.go
// version: 1.0.0
// guid: 0f6b3a92-c4d1-47e8-95b7-3d28a1e60c49
// last-edited: 2026-11-13

package coretest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations/notify/notifytest"
)

// FakeNotifier is notifytest.FakeNotifier, here for tests outside the notify
// package: a notify channel that records what it is sent and fails on script.
type FakeNotifier = notifytest.FakeNotifier

// WebhookDelivery is one request a WebhookReceiver took.
type WebhookDelivery struct {
	Method string
	Header http.Header
	Body   []byte
	// SignatureErr is nil when the request carried a valid X-Mediacore-Signature
	// for the receiver's secret, URL and the body it received, and why it did
	// not otherwise. With no secret the receiver does not look: nil.
	SignatureErr error
}

// WebhookReceiver is an httptest server that takes notify's webhook requests
// (05-notify section 3.1), records them, and checks each signature with its
// own implementation of the signing rule: it shares no code with the sender,
// so a mistake in one is not repeated in the other.
type WebhookReceiver struct {
	// URL is the address to configure as the instance's url. The signature
	// covers it as written, so a test must configure exactly this string.
	URL string
	// Secret, when non-empty, makes the receiver verify signatures.
	Secret string
	// Tolerance is how old a signature's timestamp may be; 0 is five minutes.
	Tolerance time.Duration
	// Now is the receiver's clock; nil is time.Now.
	Now func() time.Time
	// Respond decides the answer to the n-th request (1-based): a status and
	// an optional Retry-After header value. nil answers 204.
	Respond func(n int, d WebhookDelivery) (status int, retryAfter string)

	srv *httptest.Server
	mu  sync.Mutex
	got []WebhookDelivery
}

// NewWebhookReceiver starts a receiver that verifies signatures made with
// secret ("" accepts unsigned requests). It is closed when t ends.
func NewWebhookReceiver(t testing.TB, secret string) *WebhookReceiver {
	t.Helper()
	r := &WebhookReceiver{Secret: secret}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	r.URL = r.srv.URL + "/gotham/hook"
	t.Cleanup(r.srv.Close)
	return r
}

func (r *WebhookReceiver) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	d := WebhookDelivery{Method: req.Method, Header: req.Header.Clone(), Body: body}
	if r.Secret != "" {
		d.SignatureErr = r.verify(d)
	}
	r.mu.Lock()
	r.got = append(r.got, d)
	n := len(r.got)
	respond := r.Respond
	r.mu.Unlock()
	status, retry := http.StatusNoContent, ""
	if respond != nil {
		status, retry = respond(n, d)
	}
	if retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	w.WriteHeader(status)
}

// Deliveries returns a copy of every request taken, in order.
func (r *WebhookReceiver) Deliveries() []WebhookDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]WebhookDelivery(nil), r.got...)
}

// verify checks d's X-Mediacore-Signature: "t=<unix>,v1=<hex>" where v1 is
// HMAC-SHA256(secret, "<t>.<delivery>.<url>.<body>") over the delivery
// header, this receiver's URL and the exact bytes received.
func (r *WebhookReceiver) verify(d WebhookDelivery) error {
	delivery := d.Header.Get("X-Mediacore-Delivery")
	if delivery == "" {
		return errors.New("no X-Mediacore-Delivery header")
	}
	var ts, sig string
	for _, part := range strings.Split(d.Header.Get("X-Mediacore-Signature"), ",") {
		switch k, v, _ := strings.Cut(part, "="); k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	if ts == "" || sig == "" {
		return errors.New("no t= and v1= in X-Mediacore-Signature")
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("timestamp %q is not a number", ts)
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	tol := r.Tolerance
	if tol == 0 {
		tol = 5 * time.Minute
	}
	if age := now.Sub(time.Unix(secs, 0)); age > tol || age < -tol {
		return fmt.Errorf("timestamp is %s from now, past the %s tolerance", age, tol)
	}
	mac := hmac.New(sha256.New, []byte(r.Secret))
	mac.Write([]byte(ts + "." + delivery + "." + r.URL + "."))
	mac.Write(d.Body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return errors.New("the signature does not match")
	}
	return nil
}
