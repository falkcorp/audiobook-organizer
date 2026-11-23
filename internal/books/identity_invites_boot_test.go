// file: internal/books/identity_invites_boot_test.go
// version: 0.1.0
// guid: c27b9e04-6d13-4a58-8f61-3e0a5d9b7c42
// last-edited: 2026-11-22

package books

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// F4-08 B10t, real core: the invite routes books serves under core identity,
// against core's own Admin (the real RotateInvite, MarkInviteEmailDelivered and
// accept), a real session, and an SMTP server that really receives the mail.
// Nothing about core is stubbed: the invite, its flag and the accept that does
// or does not bind the address are core's.

// bootSMTP is a plaintext SMTP server on loopback (core's sender allows that with
// no username). It keeps what it was sent; failRcpt makes it refuse every
// recipient.
type bootSMTP struct {
	mu       sync.Mutex
	msgs     []string
	rcpts    []string
	failRcpt bool
}

func startBootSMTP(t *testing.T) (*bootSMTP, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	s := &bootSMTP{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s, ln.Addr().(*net.TCPAddr).Port
}

func (s *bootSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	w("220 batcave ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch strings.ToUpper(strings.SplitN(line, " ", 2)[0]) {
		case "EHLO", "HELO":
			w("250-batcave")
			w("250 8BITMIME")
		case "MAIL":
			w("250 ok")
		case "RCPT":
			s.mu.Lock()
			fail := s.failRcpt
			if !fail {
				s.rcpts = append(s.rcpts, line)
			}
			s.mu.Unlock()
			if fail {
				w("550 no such user")
			} else {
				w("250 ok")
			}
		case "DATA":
			w("354 send")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, b.String())
			s.mu.Unlock()
			w("250 queued")
		case "QUIT":
			w("221 bye")
			return
		default:
			w("502 unknown")
		}
	}
}

func (s *bootSMTP) setFail(v bool) { s.mu.Lock(); s.failRcpt = v; s.mu.Unlock() }
func (s *bootSMTP) count() int     { s.mu.Lock(); defer s.mu.Unlock(); return len(s.msgs) }

var inviteTokenInMail = regexp.MustCompile(`/invite#token=([A-Za-z0-9_-]+)`)

// lastToken is the token of the newest mail's invite link, after undoing the
// quoted-printable wrapping of the body.
func (s *bootSMTP) lastToken(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.msgs, "no mail was received")
	body := strings.ReplaceAll(s.msgs[len(s.msgs)-1], "=\r\n", "")
	body = strings.ReplaceAll(body, "=3D", "=")
	m := inviteTokenInMail.FindStringSubmatch(body)
	require.NotNil(t, m, "the mail carries no core invite link: %s", body)
	return m[1]
}

type inviteBoot struct {
	c    *composed
	h    http.Handler
	ck   *http.Cookie
	smtp *bootSMTP
}

func newInviteBoot(t *testing.T) *inviteBoot {
	t.Helper()
	smtp, port := startBootSMTP(t)
	// Mail and the public origin are operator-only settings: their layer is the
	// environment, under the canonical names the flip's rename map gives HEAD's.
	t.Setenv("ABO_INTEGRATIONS_NOTIFY_EMAIL_HOST", "127.0.0.1")
	t.Setenv("ABO_INTEGRATIONS_NOTIFY_EMAIL_PORT", strconv.Itoa(port))
	t.Setenv("ABO_INTEGRATIONS_NOTIFY_EMAIL_FROM", "batcave@example.test")
	t.Setenv("ABO_CORE_HTTP_PUBLIC_BASE_URL", "https://library.example.test")
	c := runComposedWith(t, func(cfg *config.Config) { cfg.EnableAuth = true }, nil, nil, nil).composed
	h := c.app.Handler()
	user, pass := redeemBootstrap(t, c)
	return &inviteBoot{c: c, h: h, ck: idLogin(t, h, user, pass), smtp: smtp}
}

func (b *inviteBoot) do(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	rec := idDo(t, b.h, method, path, body, b.ck)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	// Core's envelope wraps a success in "data".
	if d, ok := out["data"].(map[string]any); ok {
		out = d
	}
	return rec.Code, out
}

// accept redeems token at core's accept route.
func (b *inviteBoot) accept(t *testing.T, token string) int {
	t.Helper()
	return post(t, b.h, "/api/core/v1/auth/invites/accept",
		map[string]string{"token": token, "password": "alfred-pennyworth-1939!"}, false).Code
}

// verified reports whether the account username holds its address verified,
// from core's own user list.
func (b *inviteBoot) verified(t *testing.T, username string) (email string, verified bool) {
	t.Helper()
	rec := idDo(t, b.h, http.MethodGet, "/api/core/v1/users?limit=200", nil, b.ck)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Items []userRow `json:"items"`
		Data  struct {
			Items []userRow `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	for _, u := range append(body.Items, body.Data.Items...) {
		if u.Username == username {
			return u.Email, u.EmailVerified
		}
	}
	t.Fatalf("no user %q in %s", username, rec.Body.String())
	return "", false
}

type userRow struct {
	Username      string `json:"username"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

func (b *inviteBoot) create(t *testing.T, username, email string, send bool) (code int, token, inviteID string, resp map[string]any) {
	t.Helper()
	code, resp = b.do(t, http.MethodPost, "/api/v1/users/invite",
		map[string]any{"username": username, "role_id": "viewer", "email": email, "send_email": send})
	token, _ = resp["token"].(string)
	if inv, ok := resp["invite"].(map[string]any); ok {
		inviteID, _ = inv["id"].(string)
	}
	return code, token, inviteID, resp
}

// TestInvitesBoot_CreateWithSendMailsFlagsAndAcceptBinds: the admin creates an
// invite with send; the mail goes out with core's link, core's flag is set only
// then, and accepting the link binds the address verified.
func TestInvitesBoot_CreateWithSendMailsFlagsAndAcceptBinds(t *testing.T) {
	b := newInviteBoot(t)
	code, token, _, resp := b.create(t, "selina", "selina@example.test", true)
	require.Equal(t, http.StatusCreated, code, resp)
	require.Equal(t, true, resp["email_sent"], resp)
	require.NotEmpty(t, token)
	require.Equal(t, 1, b.smtp.count())
	require.Equal(t, token, b.smtp.lastToken(t), "the mail carries the link the admin was given")
	require.Equal(t, "https://library.example.test/invite#token="+token, resp["invite_url"])

	require.Equal(t, http.StatusOK, b.accept(t, token))
	email, verified := b.verified(t, "selina")
	require.Equal(t, "selina@example.test", email)
	require.True(t, verified, "an emailed invite from an admin binds the address verified")
}

// TestInvitesBoot_SendFailureLeavesTheAddressUnbound: a mail that did not go out
// sets no flag, so accepting the link the admin hands over by hand verifies
// nothing (core's flag is the only gate).
func TestInvitesBoot_SendFailureLeavesTheAddressUnbound(t *testing.T) {
	b := newInviteBoot(t)
	b.smtp.setFail(true)
	code, token, _, resp := b.create(t, "harvey", "harvey@example.test", true)
	require.Equal(t, http.StatusCreated, code, resp)
	require.Equal(t, false, resp["email_sent"], resp)
	require.NotEmpty(t, resp["email_error"], resp)
	require.NotEmpty(t, token, "the admin still gets the link to hand over")
	require.Equal(t, http.StatusOK, b.accept(t, token))
	_, verified := b.verified(t, "harvey")
	require.False(t, verified, "a failed send must leave the address unverified")
}

// TestInvitesBoot_ResendRotates: the resend is core's rotation, the old link is
// refused and the new one is the one that works.
func TestInvitesBoot_ResendRotates(t *testing.T) {
	b := newInviteBoot(t)
	code, oldToken, oldID, resp := b.create(t, "oswald", "oswald@example.test", false)
	require.Equal(t, http.StatusCreated, code, resp)
	require.Equal(t, 0, b.smtp.count())

	code, rot := b.do(t, http.MethodPost, "/api/v1/users/invites/"+oldID+"/resend", nil)
	require.Equal(t, http.StatusOK, code, rot)
	require.Equal(t, true, rot["email_sent"], rot)
	newToken, _ := rot["token"].(string)
	require.NotEmpty(t, newToken)
	require.NotEqual(t, oldToken, newToken, "a rotation mints a new link")
	require.Equal(t, newToken, b.smtp.lastToken(t))

	require.NotEqual(t, http.StatusOK, b.accept(t, oldToken), "the old link must be dead")
	require.Equal(t, http.StatusOK, b.accept(t, newToken))
	// The old invite was never emailed (unflagged), and a rotation does not make
	// it so: the resend's mail does not bind the address.
	_, verified := b.verified(t, "oswald")
	require.False(t, verified, "a resend of a never-flagged invite must not verify the address")
}

// TestInvitesBoot_ResendOfAFlaggedInviteStaysFlagged: an invite an admin emailed
// is flagged; its rotation is born unflagged and is flagged again only after the
// new mail went out. The per-invite limit is one send a minute and the rotation
// keeps the invitee, so the resend waits it out.
func TestInvitesBoot_ResendOfAFlaggedInviteStaysFlagged(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the one-minute per-invite resend limit")
	}
	b := newInviteBoot(t)
	code, _, id, resp := b.create(t, "edward", "edward@example.test", true)
	require.Equal(t, http.StatusCreated, code, resp)
	require.Equal(t, true, resp["email_sent"], resp)
	time.Sleep(62 * time.Second)

	code, rot := b.do(t, http.MethodPost, "/api/v1/users/invites/"+id+"/resend", nil)
	require.Equal(t, http.StatusOK, code, rot)
	require.Equal(t, true, rot["email_sent"], rot)
	require.Empty(t, rot["email_binding_error"], rot)
	require.Equal(t, 2, b.smtp.count())
	require.Equal(t, http.StatusOK, b.accept(t, b.smtp.lastToken(t)))
	_, verified := b.verified(t, "edward")
	require.True(t, verified, "the rotation of an emailed invite binds the address as the original would have")
}
