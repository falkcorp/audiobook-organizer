// file: internal/server/handlers/invite_delivery.go
// version: 1.4.0
// guid: 9d3a7f62-1c84-4e5b-a0f9-6b2e8c4d1a37
// last-edited: 2026-10-20

// Invitation delivery: the invite email, resend, and the public invite lookup
// the accept page calls.
//
// What an invite email carries: the invitee's own link, the SHARED guest
// Cloudflare service-token header pair (when configured) and the AudioBooth
// setup steps. The header pair is a credential, so:
//   - it is rendered only into the email body and into the public lookup for
//     a valid, unexpired, unused token;
//   - no admin response, error or log line ever contains it, or the rendered
//     body. Admins get only email_sent / email_error.

package handlers

import (
	"bytes"
	"context"
	"errors"
	htmltemplate "html/template"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	texttemplate "text/template"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	imail "github.com/falkcorp/audiobook-organizer/internal/mail"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

var inviteLog = logger.New("handlers.invites")

// GuestHeaders is the shared guest Cloudflare Access service token.
type GuestHeaders struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// InviteDelivery is what UserHandler needs to deliver invitations. Every field
// is read per request so tests can swap them; the server wires them from
// config (wire_handlers.go). A nil func means "not configured".
type InviteDelivery struct {
	// Sender returns the configured mail sender, or nil when SMTP is not
	// configured.
	Sender func() imail.Sender
	// BaseURL returns the public origin links are built on ("" = relative).
	BaseURL func() string
	// GuestHeaders returns the shared header pair and whether it is configured.
	GuestHeaders func() (GuestHeaders, bool)
	// GuestPolicySyncConfigured reports whether accept adds the invitee to
	// the Cloudflare Access guest policy.
	GuestPolicySyncConfigured func() bool
	// Now is the clock (tests).
	Now func() time.Time
}

func (d *InviteDelivery) sender() imail.Sender {
	if d == nil || d.Sender == nil {
		return nil
	}
	return d.Sender()
}

func (d *InviteDelivery) baseURL() string {
	if d == nil || d.BaseURL == nil {
		return ""
	}
	return strings.TrimRight(d.BaseURL(), "/")
}

// Why an invitation email cannot go out; sendBlocked returns one of these.
const (
	mailNotConfiguredReason = "email is not configured on this server (SMTP_HOST and SMTP_FROM); copy the link instead"
	publicURLMissingReason  = "the server's public address is not set (PUBLIC_BASE_URL or EXTERNAL_URL), so an emailed link would not open; copy the link instead"
)

// sendBlocked returns why an invitation email cannot be sent right now, or
// "" when it can: mail must be configured, and the link base must be an
// absolute http(s) URL, since a relative link in an email opens nothing.
func (d *InviteDelivery) sendBlocked() string {
	if d.sender() == nil {
		return mailNotConfiguredReason
	}
	u, err := url.Parse(d.baseURL())
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return publicURLMissingReason
	}
	return ""
}

func (d *InviteDelivery) headers() (GuestHeaders, bool) {
	if d == nil || d.GuestHeaders == nil {
		return GuestHeaders{}, false
	}
	return d.GuestHeaders()
}

func (d *InviteDelivery) policySync() bool {
	return d != nil && d.GuestPolicySyncConfigured != nil && d.GuestPolicySyncConfigured()
}

func (d *InviteDelivery) now() time.Time {
	if d == nil || d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// SetInviteDelivery installs the delivery dependencies.
func (h *UserHandler) SetInviteDelivery(d *InviteDelivery) { h.delivery = d }

// InviteURL is the link an invitee opens: <base>/invite/<token>.
func InviteURL(base, token string) string {
	return strings.TrimRight(base, "/") + "/invite/" + token
}

// normalizeInviteEmail returns the lowercase bare address, "" for empty input,
// or an error for anything that is not exactly one plain address.
func normalizeInviteEmail(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("email must be a single plain address")
	}
	e := util.NormalizeEmail(raw)
	if e == "" {
		return "", nil
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || len(e) > 254 {
		return "", errors.New("email must be a single plain address, like name@example.com")
	}
	return e, nil
}

// ── Email template ──────────────────────────────────────────────────────────

type inviteEmailData struct {
	Username  string
	InviteURL string
	ServerURL string
	ExpiresAt string
	Headers   *GuestHeaders
}

const inviteEmailSubject = "You're invited to Audiobook Organizer"

var inviteTextTmpl = texttemplate.Must(texttemplate.New("text").Parse(`Hello {{.Username}},

You have been invited to Audiobook Organizer.

1. Open your invitation link and choose a password:

   {{.InviteURL}}

   The link works once and expires {{.ExpiresAt}}.
{{if .Headers}}
2. Set up the AudioBooth app on your phone:

   a. Open AudioBooth and go to Servers. Tap + to add a server.
   b. Server address: {{.ServerURL}}
   c. BEFORE signing in, tap Custom Headers and add these two headers,
      exactly as written:

      Name:  CF-Access-Client-Id
      Value: {{.Headers.ClientID}}

      Name:  CF-Access-Client-Secret
      Value: {{.Headers.ClientSecret}}

   d. Go back. The Custom Headers row should read 2.
   e. Sign in with your username ({{.Username}}) and the password you chose.

These two header values are shared guest credentials for reaching the
server. They do not sign you in; your own password does. Please do not
share them.
{{end}}
If you were not expecting this invitation, you can ignore this email.
`))

var inviteHTMLTmpl = htmltemplate.Must(htmltemplate.New("html").Parse(`<!doctype html>
<html><body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;line-height:1.5;color:#1d1d1f;max-width:600px">
<p>Hello {{.Username}},</p>
<p>You have been invited to <strong>Audiobook Organizer</strong>.</p>
<h3>1. Choose your password</h3>
<p><a href="{{.InviteURL}}">Open your invitation</a> and choose a password.<br>
If the link does not open, copy this address into your browser:<br>
<code>{{.InviteURL}}</code></p>
<p>The link works once and expires {{.ExpiresAt}}.</p>
{{if .Headers}}
<h3>2. Set up AudioBooth on your phone</h3>
<ol>
<li>Open AudioBooth and go to <strong>Servers</strong>. Tap <strong>+</strong> to add a server.</li>
<li>Server address: <code>{{.ServerURL}}</code></li>
<li><strong>Before signing in</strong>, tap <strong>Custom Headers</strong> and add these two headers, exactly as written:
<table style="border-collapse:collapse;margin:8px 0">
<tr><td style="padding:4px 8px;border:1px solid #ccc">Name</td><td style="padding:4px 8px;border:1px solid #ccc"><code>CF-Access-Client-Id</code></td></tr>
<tr><td style="padding:4px 8px;border:1px solid #ccc">Value</td><td style="padding:4px 8px;border:1px solid #ccc"><code>{{.Headers.ClientID}}</code></td></tr>
<tr><td style="padding:4px 8px;border:1px solid #ccc">Name</td><td style="padding:4px 8px;border:1px solid #ccc"><code>CF-Access-Client-Secret</code></td></tr>
<tr><td style="padding:4px 8px;border:1px solid #ccc">Value</td><td style="padding:4px 8px;border:1px solid #ccc"><code>{{.Headers.ClientSecret}}</code></td></tr>
</table></li>
<li>Go back. The Custom Headers row should read <strong>2</strong>.</li>
<li>Sign in with your username (<strong>{{.Username}}</strong>) and the password you chose.</li>
</ol>
<p>These two header values are shared guest credentials for reaching the server. They do not sign you in; your own password does. Please do not share them.</p>
{{end}}
<p style="color:#666">If you were not expecting this invitation, you can ignore this email.</p>
</body></html>`))

// renderInviteEmail builds the invitation message. Templates escape every
// value (html/template contextually; the text part has no markup).
func renderInviteEmail(inv *database.Invite, inviteURL, serverURL string, headers *GuestHeaders) (imail.Message, error) {
	data := inviteEmailData{
		Username:  inv.Username,
		InviteURL: inviteURL,
		ServerURL: serverURL,
		ExpiresAt: inv.ExpiresAt.UTC().Format("Mon 2 Jan 2006 15:04 UTC"),
		Headers:   headers,
	}
	var text, html bytes.Buffer
	if err := inviteTextTmpl.Execute(&text, data); err != nil {
		return imail.Message{}, err
	}
	if err := inviteHTMLTmpl.Execute(&html, data); err != nil {
		return imail.Message{}, err
	}
	return imail.Message{To: inv.Email, Subject: inviteEmailSubject, Text: text.String(), HTML: html.String()}, nil
}

// ── Rate limiting ───────────────────────────────────────────────────────────

// Resend limits: one email per invite per minute, and per admin a burst of 10
// refilling one every 6 minutes (about 10 an hour). A send at create time
// counts against both.
const (
	invitePerTokenEvery = time.Minute
	invitePerAdminEvery = 6 * time.Minute
	invitePerAdminBurst = 10
	inviteLimiterMax    = 4096
)

// keyedLimiter is a bounded map of token buckets.
// Not safe on its own: inviteSendLimiter holds its lock around every use.
type keyedLimiter struct {
	every time.Duration
	burst int
	m     map[string]*rate.Limiter
}

func newKeyedLimiter(every time.Duration, burst int) *keyedLimiter {
	return &keyedLimiter{every: every, burst: burst, m: map[string]*rate.Limiter{}}
}

func (k *keyedLimiter) get(key string) *rate.Limiter {
	l, ok := k.m[key]
	if !ok {
		if len(k.m) >= inviteLimiterMax {
			// Drop full buckets first; a full bucket is what a new key gets anyway.
			for kk, ll := range k.m {
				if ll.Tokens() >= float64(k.burst) {
					delete(k.m, kk)
				}
			}
		}
		l = rate.NewLimiter(rate.Every(k.every), k.burst)
		k.m[key] = l
	}
	return l
}

// inviteSendLimiter enforces both limits atomically: a send is allowed only
// when both buckets have a token, and then takes one from each.
type inviteSendLimiter struct {
	mu       sync.Mutex
	perToken *keyedLimiter
	perAdmin *keyedLimiter
}

func newInviteSendLimiter() *inviteSendLimiter {
	return &inviteSendLimiter{
		perToken: newKeyedLimiter(invitePerTokenEvery, 1),
		perAdmin: newKeyedLimiter(invitePerAdminEvery, invitePerAdminBurst),
	}
}

// allow returns "" when the send may proceed, else the reason.
func (l *inviteSendLimiter) allow(token, adminID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.perToken.get(token)
	a := l.perAdmin.get(adminID)
	if t.Tokens() < 1 {
		return "this invitation was emailed less than a minute ago; wait a minute and try again"
	}
	if a.Tokens() < 1 {
		return "too many invitation emails sent recently; try again later"
	}
	t.Allow()
	a.Allow()
	return ""
}

func currentUserID(c *gin.Context) string {
	if u, ok := servermiddleware.CurrentUser(c); ok && u != nil {
		return u.ID
	}
	return ""
}

// ── Sending ────────────────────────────────────────────────────────────────

// sendInviteEmail renders and sends the invitation for inv and records the
// send. It returns a message safe to show an admin: never the body, never the
// header pair.
func (h *UserHandler) sendInviteEmail(ctx context.Context, inv *database.Invite) (sentAt *time.Time, errMsg string) {
	if why := h.delivery.sendBlocked(); why != "" {
		return nil, why
	}
	sender := h.delivery.sender()
	if inv.Email == "" {
		return nil, "this invitation has no email address"
	}
	base := h.delivery.baseURL()
	var hdr *GuestHeaders
	if g, ok := h.delivery.headers(); ok {
		hdr = &g
	}
	msg, err := renderInviteEmail(inv, InviteURL(base, inv.Token), base, hdr)
	if err != nil {
		inviteLog.Error("invite email: render failed for invite of %s: %v", logger.SanitizeLogValue(inv.Username), err)
		return nil, "could not build the invitation email"
	}
	if err := sender.Send(ctx, msg); err != nil {
		inviteLog.Warn("invite email: send failed for invite of %s: %v", logger.SanitizeLogValue(inv.Username), logger.SanitizeLogValue(err.Error()))
		return nil, "sending failed: " + err.Error()
	}
	now := h.delivery.now()
	if h.deliveryStore != nil {
		if _, err := h.deliveryStore.MarkInviteEmailSent(inv.Token, msg.To, now); err != nil {
			// The mail went out; only the bookkeeping failed (or the invite was
			// accepted meanwhile). Report the send, log the rest.
			inviteLog.Warn("invite email: sent, but recording it failed for invite of %s: %v", logger.SanitizeLogValue(inv.Username), err)
		}
	}
	inviteLog.Info("invite email: sent invitation for %s", logger.SanitizeLogValue(inv.Username))
	return &now, ""
}

// ResendInvite handles POST /api/v1/users/invites/:token/resend.
func (h *UserHandler) ResendInvite(c *gin.Context) {
	token := c.Param("token")
	inv, err := h.store.GetInvite(token)
	if err != nil {
		httputil.InternalError(c, "failed to load invite", err)
		return
	}
	if inv == nil || inv.UsedAt != nil || h.delivery.now().After(inv.ExpiresAt) {
		httputil.RespondWithNotFound(c, "invite", "")
		return
	}
	if inv.Email == "" {
		httputil.RespondWithBadRequest(c, "this invitation has no email address; delete it and create a new one with an email")
		return
	}
	switch h.delivery.sendBlocked() {
	case "":
	case mailNotConfiguredReason:
		httputil.RespondWithError(c, http.StatusServiceUnavailable, "email is not configured on this server", "MAIL_NOT_CONFIGURED")
		return
	default:
		httputil.RespondWithError(c, http.StatusServiceUnavailable, publicURLMissingReason, "PUBLIC_URL_NOT_CONFIGURED")
		return
	}
	if why := h.sendLimiter.allow(token, currentUserID(c)); why != "" {
		httputil.RespondWithError(c, http.StatusTooManyRequests, why, "TOO_MANY_REQUESTS")
		return
	}
	sentAt, errMsg := h.sendInviteEmail(c.Request.Context(), inv)
	if errMsg != "" {
		httputil.RespondWithError(c, http.StatusBadGateway, errMsg, "MAIL_SEND_FAILED")
		return
	}
	httputil.RespondWithOK(c, gin.H{"email_sent": true, "email_sent_at": sentAt})
}

// ── Public lookup ──────────────────────────────────────────────────────────

// PublicInvite handles GET /api/v1/auth/invite/:token (public, rate-limited at
// the route). It answers only for a token accept would take, and then with
// only the username, the expiry and, when configured, the guest header pair.
// Every case accept refuses (missing, used, expired, the username taken since,
// the invited address held verified by another account) gets the same 404:
// the store runs accept's own checks (InviteAcceptChecker), so a link holder
// cannot learn from a 200 here followed by a refused accept that a username or
// an address has an account.
func (h *UserHandler) PublicInvite(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	token := c.Param("token")
	notFound := func() {
		httputil.RespondWithError(c, http.StatusNotFound, "this invitation is invalid, already used or expired", "INVITE_NOT_FOUND")
	}
	if len(token) < 16 || len(token) > 128 {
		notFound()
		return
	}
	inv, err := h.store.GetInvite(token)
	if err != nil {
		httputil.InternalError(c, "failed to load invite", err)
		return
	}
	if inv == nil || inv.UsedAt != nil || h.delivery.now().After(inv.ExpiresAt) {
		notFound()
		return
	}
	if h.acceptChecker != nil {
		if err := h.acceptChecker.CheckInviteAcceptable(token); database.IsInviteRefusal(err) {
			notFound()
			return
		} else if err != nil {
			httputil.InternalError(c, "failed to check invite", err)
			return
		}
	}
	resp := gin.H{"username": inv.Username, "expires_at": inv.ExpiresAt}
	if g, ok := h.delivery.headers(); ok {
		resp["guest_headers"] = g
	}
	httputil.RespondWithOK(c, resp)
}

// acceptedInviteView is the admin's view of an accepted invite: no token.
type acceptedInviteView struct {
	Username          string     `json:"username"`
	Email             string     `json:"email,omitempty"`
	AcceptedAt        *time.Time `json:"accepted_at"`
	AcceptedUserID    string     `json:"accepted_user_id,omitempty"`
	EmailBinding      string     `json:"email_binding,omitempty"`
	SSOGrant          string     `json:"sso_grant,omitempty"`
	GuestPolicyStatus string     `json:"guest_policy_status,omitempty"`
	GuestPolicyDetail string     `json:"guest_policy_detail,omitempty"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at,omitempty"`
	// DisplacedUserIDs are accounts that had typed this address into their
	// profile and lost it to the accept.
	DisplacedUserIDs []string `json:"displaced_user_ids,omitempty"`
}

// acceptedInviteWindow is how far back ListInvites reports accepted invites.
const acceptedInviteWindow = 30 * 24 * time.Hour

func (h *UserHandler) acceptedInvites() []acceptedInviteView {
	out := []acceptedInviteView{}
	if h.deliveryStore == nil {
		return out
	}
	list, err := h.deliveryStore.ListAcceptedInvitesSince(h.delivery.now().Add(-acceptedInviteWindow))
	if err != nil {
		inviteLog.Warn("list accepted invites: %v", err)
		return out
	}
	for _, i := range list {
		out = append(out, acceptedInviteView{
			Username: i.Username, Email: i.Email, AcceptedAt: i.UsedAt, AcceptedUserID: i.AcceptedUserID,
			EmailBinding: i.EmailBinding, SSOGrant: i.SSOGrant,
			GuestPolicyStatus: i.GuestPolicyStatus, GuestPolicyDetail: i.GuestPolicyDetail, GuestPolicyAt: i.GuestPolicyAt,
			DisplacedUserIDs: i.DisplacedUserIDs,
		})
	}
	return out
}

// GuestAccessRevoker ends the invite-granted SSO access (and, when it was added
// by the sync, the Cloudflare guest-policy entry) for an email a user no longer
// holds. Called after a deactivation or an email change; best-effort, it never
// fails the request that triggered it.
type GuestAccessRevoker func(userID, email, reason string)

// SetGuestAccessRevoker installs the revoker DeactivateUser calls.
func (h *UserHandler) SetGuestAccessRevoker(r GuestAccessRevoker) { h.revokeGuestAccess = r }

// SetGuestAccessRevoker installs the revoker UpdateMe calls when the email
// changes.
func (h *AuthHandler) SetGuestAccessRevoker(r GuestAccessRevoker) { h.revokeGuestAccess = r }
