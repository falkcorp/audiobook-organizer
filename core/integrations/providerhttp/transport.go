// file: core/integrations/providerhttp/transport.go
// version: 1.0.0
// guid: 251805ba-baff-4a97-b673-26771486637d
// last-edited: 2026-11-11

package providerhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// classifyBodyMax is how much of an error response the transport reads to
// classify it: the body names the problem the status leaves ambiguous.
const classifyBodyMax = 64 << 10

// transport is the RoundTripper of every Client (05-metadata M2.1, M2.4).
// EVERYTHING lives here, not in Client.Do: an SDK that calls Client(), copies
// the *http.Client and sets its own CheckRedirect (the Deluge adapter) still
// passes the gate, the dial guards, the quota and the retry rules, because
// they are in the transport the copy keeps.
//
// Per request: the disabled switch, ctx, the hold, the breaker (admit); then
// per attempt the bucket, the quota, the egress choice and dial guard, the
// per-attempt timeout, the send, the classification and, for a Retryable
// class, a back-off and the next attempt. The breaker and the hold learn the
// request's FINAL outcome once (a retry spends quota and a token like the
// first try, but is not a second failure).
type transport struct{ g *gate }

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	g, r := t.g, t.g.r
	ctx := req.Context()
	fail := func(err error) (*http.Response, error) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	if err := checkRequestURL(req.URL); err != nil {
		r.met.refused(g.ref, ReasonEgress)
		return fail(refuse(g.ref, "http", ReasonEgress, time.Time{}, err))
	}
	tk, err := g.admit(ctx, "http")
	if err != nil {
		return fail(err)
	}
	lim := g.sc.limits()
	replayable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	maxRetries := lim.MaxRetries
	if !replayable || tk.probe { // never resend an empty body; a probe is one request
		maxRetries = 0
	}
	maxBody := g.pol.MaxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}

	var last outcome
	var haveLast bool
	finishLast := func() {
		if haveLast {
			tk.finish(last)
		} else {
			tk.abort()
		}
	}
	for attempt := 0; ; attempt++ {
		counted, err := g.pace(ctx, "", "http")
		if err != nil {
			finishLast()
			return fail(err)
		}
		actx, cancel := ctx, context.CancelFunc(func() {})
		if lim.Timeout > 0 { // per attempt, started after the bucket wait (M2.6)
			actx, cancel = context.WithTimeout(ctx, lim.Timeout)
		}
		r2 := req.Clone(actx)
		if attempt > 0 && req.GetBody != nil {
			b, berr := req.GetBody()
			if berr != nil {
				cancel()
				finishLast()
				return fail(berr)
			}
			r2.Body = b
		}
		if r.o.UserAgent != "" && r2.Header.Get("User-Agent") == "" {
			r2.Header.Set("User-Agent", r.o.UserAgent)
		}
		resp, rerr := t.pick(r2).RoundTrip(r2)
		if rerr != nil {
			cancel()
			if isEgressRefusal(rerr) {
				r.met.refused(g.ref, ReasonEgress)
				g.sc.refund(ctx, counted) // refused at the dial: nothing was sent
				tk.finish(outcome{class: integrations.ClassRefusedLocal})
				return nil, refuse(g.ref, "http", ReasonEgress, time.Time{}, rerr)
			}
			o := outcome{class: classifyErr(ctx, rerr)}
			r.met.request(g.ref, o.class)
			last, haveLast = o, true
			if o.class.Retryable() && attempt < maxRetries && ctx.Err() == nil {
				if err := t.backoff(ctx, attempt, 0, lim); err != nil {
					tk.finish(o)
					return nil, canceled(g.ref, "http", err)
				}
				continue
			}
			tk.finish(o)
			return nil, rerr
		}

		o := outcome{status: resp.StatusCode, success: resp.StatusCode < 400}
		var head []byte
		if !o.success {
			var rerr error
			head, rerr = io.ReadAll(io.LimitReader(resp.Body, classifyBodyMax))
			if rerr != nil {
				head = nil // a body that cannot be read classifies by its status alone
			}
			o.class = classifyStatus(resp.StatusCode, head)
			o.retryAfter = retryAfterOf(resp.Header, r.now())
		}
		r.met.request(g.ref, o.class)
		last, haveLast = o, true
		if !o.success && o.class.Retryable() && attempt < maxRetries && ctx.Err() == nil {
			finishBody(resp.Body)
			cancel()
			if err := t.backoff(ctx, attempt, o.retryAfter, lim); err != nil {
				tk.finish(o)
				return nil, canceled(g.ref, "http", err)
			}
			continue
		}
		tk.finish(o)
		if resp.ContentLength > maxBody {
			_ = resp.Body.Close()
			cancel()
			return nil, &integrations.Error{Ref: g.ref, Op: "http", Class: integrations.ClassBadOutput,
				Status: resp.StatusCode, Err: ErrResponseTooLarge}
		}
		var body io.Reader = resp.Body
		if len(head) > 0 {
			body = io.MultiReader(bytes.NewReader(head), resp.Body)
		}
		resp.Body = &guardBody{r: body, c: resp.Body, cancel: cancel, left: maxBody}
		return resp, nil
	}
}

// backoff sleeps before the next attempt: the server's Retry-After clamped to
// the instance's RetryAfterMax, else 1s, 2s, 4s ... capped at 30s with up to
// 50% jitter (so throttled workers do not retry in lockstep).
func (t *transport) backoff(ctx context.Context, attempt int, retryAfter time.Duration, lim integrations.Limits) error {
	d := retryAfter
	if d > 0 {
		d = min(d, lim.RetryAfterMax)
	} else {
		base := min(time.Second<<min(attempt, 5), 30*time.Second)
		d = base + t.g.r.jitter(base)
	}
	t.g.r.met.retry(t.g.ref)
	return t.g.r.sleep(ctx, d)
}

// pick chooses the guarded transport of one attempt. The private-capable one
// is used only when the instance may reach this endpoint privately: never for
// EgressPublicOnly (data-derived URLs), never for a redirect hop to another
// endpoint than the one that redirected (SEC4-15: Local is bound to the
// instance's configured origins and must not carry to another LAN host), and
// only where Policy.Private says the endpoint is bound or allow-listed. Every
// other request uses the public-only transport, whose dial refuses private
// addresses, so an untrusted request can never reuse a connection dialled
// under trust.
func (t *transport) pick(req *http.Request) *http.Transport {
	g := t.g
	if g.pol.Egress == integrations.EgressPublicOnly || g.pol.Private == nil {
		return g.r.strict
	}
	if res := req.Response; res != nil && (res.Request == nil || endpoint(res.Request.URL) != endpoint(req.URL)) {
		return g.r.strict
	}
	if g.pol.Private(endpoint(req.URL)) {
		return g.r.lan
	}
	return g.r.strict
}

// endpoint is u's lower-case "host:port" with the scheme's default port.
func endpoint(u *url.URL) string {
	if u == nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// checkRequestURL is the positive scheme allowlist (http and https) and a
// non-empty host, on every request: a copied client that dropped
// CheckRedirect still cannot reach file://, gopher:// or dict://.
func checkRequestURL(u *url.URL) error {
	if u == nil {
		return ErrInvalidURL
	}
	return ValidateParsedURL(u)
}

// isEgressRefusal reports a refusal made by the dial guard or the scheme check.
func isEgressRefusal(err error) bool {
	return errors.Is(err, ErrBlockedAddress) || errors.Is(err, ErrBlockedScheme) || errors.Is(err, ErrInvalidURL)
}

// guardBody bounds a response body and ties the attempt's timeout to its life:
// the deadline must outlive RoundTrip (the caller reads the body after it
// returns), so it is released on Close, not on return.
type guardBody struct {
	r      io.Reader
	c      io.Closer
	cancel context.CancelFunc
	left   int64
}

func (b *guardBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var one [1]byte
		n, err := b.r.Read(one[:])
		if n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *guardBody) Close() error {
	err := b.c.Close()
	b.cancel()
	return err
}

// finishBody drains a bounded rest of body so its connection is reused, then
// closes it. It reports whether both worked; a body that cannot be drained or
// closed only costs the next attempt a new connection, so a caller that is not
// asking ignores the answer.
func finishBody(body io.ReadCloser) (reusable bool) {
	_, drainErr := io.Copy(io.Discard, io.LimitReader(body, classifyBodyMax))
	closeErr := body.Close()
	return drainErr == nil && closeErr == nil
}
