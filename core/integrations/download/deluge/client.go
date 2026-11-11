// file: core/integrations/download/deluge/client.go
// version: 1.1.0
// guid: c1a8d4f7-2e69-4b03-9d5a-7f3e6b0a1c82
// last-edited: 2026-11-11

package deluge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// The Deluge Web JSON-RPC client: POST <base>/json, JSON-RPC 2.0 shaped,
// session-cookie auth. It is the port of internal/deluge/client.go (backlog
// 6.1) with three changes (05-download §6, §10): every method takes a ctx,
// the requests leave through the instance's integrations.HTTP, and the
// 5-minute http.Client timeout is a per-call limit (callTimeout) under the
// caller's own deadline. The generation-guarded re-login is unchanged.
// RemoveTorrent, which HEAD has no caller for, is not ported (phase 2's
// Remover).
//
// Reference: https://deluge.readthedocs.io/en/latest/reference/webapi.html

// callTimeout bounds one request, login included. It equals the adapter's
// Limits.Timeout (05-download §7), which the outbound client enforces per
// attempt as well; the bound here keeps the adapter honest when it runs under
// a client that does not.
const callTimeout = 30 * time.Second

// maxBody bounds a response the client reads: a seedbox of tens of
// thousands of torrents answers a listing in a few MiB.
const maxBody = 64 << 20

// detailChunkSize is how many torrent IDs one core.get_torrents_status asks
// for (HEAD's rule).
const detailChunkSize = 50

// Deluge json_api error codes: 1 is NotAuthorizedError, raised before the
// method runs; 4 is the generic method-failure code carrying the traceback
// text.
const (
	rpcCodeNotAuthenticated = 1
	rpcCodeMethodFailure    = 4
)

var (
	// ErrTorrentNotFound means Deluge does not know the torrent (already
	// removed), as opposed to Deluge being unreachable.
	ErrTorrentNotFound = errors.New("deluge: torrent not found")
	// errLoginRejected: auth.login answered false (a wrong password).
	errLoginRejected = errors.New("deluge: auth.login rejected the credentials")
)

type rpcRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int64  `json:"id"`
}

type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpcError is a Deluge JSON-RPC error object. call returns it (as *rpcError)
// for server-reported errors only, so transport and decode failures can never
// be mistaken for one.
type rpcError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("deluge error %d", e.Code)
}

// statusError is a non-200 answer of the Web UI (a proxy page, a 5xx).
type statusError struct {
	Status  int
	Excerpt string
}

func (e *statusError) Error() string { return fmt.Sprintf("deluge: HTTP status %d", e.Status) }

// decodeError is an answer that is not the JSON-RPC document it should be.
type decodeError struct {
	What    string
	Err     error
	Excerpt string
}

func (e *decodeError) Error() string { return fmt.Sprintf("deluge: decode %s: %v", e.What, e.Err) }
func (e *decodeError) Unwrap() error { return e.Err }

// secretError is a password the Registry would not serve (missing, withheld,
// unreadable): nothing was sent.
type secretError struct{ Err error }

func (e *secretError) Error() string { return "deluge: password unavailable" }
func (e *secretError) Unwrap() error { return e.Err }

// rpc is one instance's connection.
type rpc struct {
	endpoint string // <base>/json
	u        *url.URL
	http     integrations.HTTP
	secrets  integrations.SecretReader
	jar      *cookiejar.Jar

	mu     sync.Mutex
	authed bool
	// loginGen counts successful logins, guarded by mu; callAuthed uses it to
	// avoid re-logging in when a concurrent caller already did.
	loginGen uint64
	reqID    atomic.Int64
}

// newRPC builds the connection to base (an http or https URL without
// userinfo). It does no I/O.
func newRPC(base string, h integrations.HTTP, s integrations.SecretReader) (*rpc, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: the Deluge web URL must be an http or https URL without credentials", integrations.ErrInvalid)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	u.RawQuery, u.Fragment = "", ""
	return &rpc{
		endpoint: strings.TrimRight(u.String(), "/") + "/json",
		u:        u, http: h, secrets: s, jar: jar,
	}, nil
}

// refuseRedirect is the CheckRedirect of every request this client sends: the
// Web UI never redirects a JSON-RPC call, and following one would replay the
// body (auth.login carries the password) to a host nobody configured.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// send makes one request through the instance's outbound client. The client is
// the HTTP's own (its transport runs the gate and the dial guards) copied with
// a redirect policy that follows nothing and no cookie jar, so a 3xx comes back
// as the answer and never as a second request. An HTTP that offers no client
// is used through Do; it is then the HTTP's own redirect policy that applies.
func (c *rpc) send(req *http.Request) (*http.Response, error) {
	if base := c.http.Client(); base != nil {
		cl := *base
		cl.CheckRedirect = refuseRedirect
		cl.Jar = nil
		return cl.Do(req)
	}
	return c.http.Do(req)
}

// call sends one JSON-RPC request and decodes the result. The request is
// bounded by callTimeout and by ctx, whichever ends first.
func (c *rpc) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	return c.callRedacting(ctx, "", method, params...)
}

// callRedacting is call for a request whose params hold a secret: any answer
// text kept in an error has the secret replaced, so a server that echoes the
// request back cannot carry it into an error, a log line or a Detail.
func (c *rpc) callRedacting(ctx context.Context, secret, method string, params ...any) (json.RawMessage, error) {
	scrub := func(s string) string {
		if secret == "" {
			return s
		}
		return strings.ReplaceAll(s, secret, "[redacted]")
	}
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(rpcRequest{Method: method, Params: params, ID: c.reqID.Add(1)})
	if err != nil {
		return nil, fmt.Errorf("deluge rpc %s: %w", method, err)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("deluge rpc %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range c.jar.Cookies(c.u) {
		req.AddCookie(ck)
	}
	resp, err := c.send(req)
	if err != nil {
		return nil, fmt.Errorf("deluge rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	c.jar.SetCookies(c.u, resp.Cookies())

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("deluge rpc %s: read response: %w", method, err)
	}
	if len(raw) > maxBody {
		return nil, &decodeError{What: method + " response", Err: errors.New("response too large")}
	}
	// Anything but 200 is a statusError, a 3xx included: the Location is never
	// followed and never put in the error.
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{Status: resp.StatusCode, Excerpt: scrub(excerpt(raw))}
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &decodeError{What: method + " response", Err: err, Excerpt: scrub(excerpt(raw))}
	}
	if out.Error != nil {
		out.Error.Message = scrub(out.Error.Message)
		return nil, out.Error
	}
	return out.Result, nil
}

// excerpt is the start of a body, for an error's Detail.
func excerpt(raw []byte) string { return string(raw[:min(200, len(raw))]) }

// callAuthed logs in if needed, then calls; on a "Not authenticated" error it
// clears the cached login, logs in again and replays the request once.
// auth.login is never replayed.
func (c *rpc) callAuthed(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	gen := c.loginGen
	c.mu.Unlock()
	result, err := c.call(ctx, method, params...)
	if err == nil || !isAuthError(err) {
		return result, err
	}
	// Clear the cached login only if nobody has logged in since this request
	// was sent; otherwise a concurrent caller already refreshed the session
	// and login below is a no-op.
	c.mu.Lock()
	if c.loginGen == gen {
		c.authed = false
	}
	c.mu.Unlock()
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	return c.call(ctx, method, params...)
}

// isAuthError is true only for a Deluge RPC error with the not-authenticated
// code. Transport and decode errors (including a proxy page whose body happens
// to contain the phrase) are never auth errors.
func isAuthError(err error) bool {
	re, ok := errors.AsType[*rpcError](err)
	return ok && re.Code == rpcCodeNotAuthenticated
}

// isUnknownTorrentError reports a method-failure RPC error for a torrent id
// Deluge does not hold: torrentmanager raises
// InvalidTorrentError('torrent_id %s not in session.'), and move_storage on an
// unknown id surfaces its KeyError.
func isUnknownTorrentError(err error) bool {
	re, ok := errors.AsType[*rpcError](err)
	if !ok || re.Code != rpcCodeMethodFailure {
		return false
	}
	m := strings.ToLower(re.Message)
	return strings.Contains(m, "invalidtorrenterror") || strings.Contains(m, "not in session") || strings.Contains(m, "keyerror")
}

// NormalizeTorrentID lowercases a torrent id and accepts only a 40 (SHA-1) or
// 64 (SHA-256) character hex string, so an empty or malformed id can never
// reach Deluge.
func NormalizeTorrentID(s string) (string, error) {
	if n := len(s); n != 40 && n != 64 {
		return "", fmt.Errorf("deluge: invalid torrent id: want 40 or 64 hex characters, got %d characters", n)
	}
	out := strings.ToLower(s)
	for i := 0; i < len(out); i++ {
		ch := out[i]
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return "", errors.New("deluge: invalid torrent id: non-hex character")
		}
	}
	return out, nil
}

// login authenticates with the Web UI, at most once per session: concurrent
// callers wait on the one in flight. The password is read when it is needed,
// so a rotation takes effect on the next login. The round trip is bounded by
// callTimeout; a caller queued behind it waits that long at most.
func (c *rpc) login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authed {
		return nil
	}
	pw, err := c.secrets.Secret(ctx, "password")
	if err != nil {
		return &secretError{Err: err}
	}
	result, err := c.callRedacting(ctx, pw.Reveal(), "auth.login", pw.Reveal())
	if err != nil {
		return fmt.Errorf("auth.login: %w", err)
	}
	var ok bool
	if err := json.Unmarshal(result, &ok); err != nil || !ok {
		return fmt.Errorf("auth.login: %w", errLoginRejected)
	}
	c.authed = true
	c.loginGen++
	return nil
}

// torrentStatus is the per-torrent field set the adapter reads. Deluge
// reports progress in percent (0..100), times as Unix seconds (maybe
// fractional, 0 = unknown) and ratio -1 while nothing was downloaded.
type torrentStatus struct {
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	SavePath      string  `json:"save_path"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	Label         string  `json:"label"` // from the Label plugin; "" when it is not installed or the torrent has none
	TotalSize     int64   `json:"total_size"`
	TotalDone     int64   `json:"total_done"`
	Ratio         float64 `json:"ratio"`
	TimeAdded     float64 `json:"time_added"`
	CompletedTime float64 `json:"completed_time"`
	IsFinished    bool    `json:"is_finished"`
	ETA           float64 `json:"eta"`
	Message       string  `json:"message"`
}

// itemFields is the field set an Items listing asks for. Files are not in it:
// they are heavy and only Files reads them.
func itemFields() []string {
	return []string{
		"hash", "name", "save_path", "state", "progress", "label", "total_size", "total_done",
		"ratio", "time_added", "completed_time", "is_finished", "eta", "message",
	}
}

// fileFields is the field set Files asks for.
func fileFields() []string {
	return []string{"hash", "save_path", "files", "file_progress", "file_priorities"}
}

// torrentFile is one entry of Deluge's `files` status field; Path is
// relative to the torrent's save_path.
type torrentFile struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
}

// fileDetail is the answer of a Files read.
type fileDetail struct {
	Hash           string        `json:"hash"`
	SavePath       string        `json:"save_path"`
	Files          []torrentFile `json:"files"`
	FileProgress   []float64     `json:"file_progress"`
	FilePriorities []int         `json:"file_priorities"`
}

// statuses is core.get_torrents_status over the torrents filter selects.
func (c *rpc) statuses(ctx context.Context, filter map[string]any) (map[string]torrentStatus, error) {
	result, err := c.callAuthed(ctx, "core.get_torrents_status", filter, itemFields())
	if err != nil {
		return nil, err
	}
	var m map[string]torrentStatus
	if err := json.Unmarshal(result, &m); err != nil {
		return nil, &decodeError{What: "torrents", Err: err, Excerpt: excerpt(result)}
	}
	for k, v := range m {
		if v.Hash == "" {
			v.Hash = k
			m[k] = v
		}
	}
	return m, nil
}

// statusesByID reads the torrents of ids (already normalized), at most
// detailChunkSize per request. Ids Deluge does not know are omitted from the
// answer. (HEAD's ListTorrentDetails, kept as the helper of an id filter.)
func (c *rpc) statusesByID(ctx context.Context, ids []string) (map[string]torrentStatus, error) {
	out := make(map[string]torrentStatus, len(ids))
	for chunk := range slices.Chunk(ids, detailChunkSize) {
		m, err := c.statuses(ctx, map[string]any{"id": chunk})
		if err != nil {
			return nil, err
		}
		maps.Copy(out, m)
	}
	return out, nil
}

// files reads one torrent's files; an unknown torrent is ErrTorrentNotFound.
func (c *rpc) files(ctx context.Context, id string) (fileDetail, error) {
	result, err := c.callAuthed(ctx, "core.get_torrent_status", id, fileFields())
	if err != nil {
		if isUnknownTorrentError(err) {
			return fileDetail{}, fmt.Errorf("%w: %s", ErrTorrentNotFound, id)
		}
		return fileDetail{}, err
	}
	var d fileDetail
	if err := json.Unmarshal(result, &d); err != nil {
		return fileDetail{}, &decodeError{What: "torrent files", Err: err, Excerpt: excerpt(result)}
	}
	if d.Hash == "" {
		return fileDetail{}, fmt.Errorf("%w: %s", ErrTorrentNotFound, id)
	}
	return d, nil
}

// moveStorage moves torrents' data to dest on the client's disk
// (core.move_storage), the "follow the book" call of library centralization.
func (c *rpc) moveStorage(ctx context.Context, ids []string, dest string) error {
	_, err := c.callAuthed(ctx, "core.move_storage", ids, dest)
	if err != nil && isUnknownTorrentError(err) {
		return fmt.Errorf("%w: %v", ErrTorrentNotFound, ids)
	}
	return err
}

// connected asks whether the Web UI is connected to a daemon.
func (c *rpc) connected(ctx context.Context) (bool, error) {
	result, err := c.callAuthed(ctx, "web.connected")
	if err != nil {
		return false, err
	}
	var ok bool
	if err := json.Unmarshal(result, &ok); err != nil {
		return false, &decodeError{What: "web.connected", Err: err, Excerpt: excerpt(result)}
	}
	return ok, nil
}

// connectFirstHost connects the Web UI to the first daemon it lists.
func (c *rpc) connectFirstHost(ctx context.Context) error {
	result, err := c.callAuthed(ctx, "web.get_hosts")
	if err != nil {
		return err
	}
	// Each host is [id, host, port, status].
	var hosts [][]any
	if err := json.Unmarshal(result, &hosts); err != nil {
		return &decodeError{What: "web.get_hosts", Err: err, Excerpt: excerpt(result)}
	}
	if len(hosts) == 0 || len(hosts[0]) == 0 {
		return &decodeError{What: "web.get_hosts", Err: errors.New("the Web UI lists no daemon")}
	}
	id, ok := hosts[0][0].(string)
	if !ok || id == "" {
		return &decodeError{What: "web.get_hosts", Err: errors.New("the first host has no id")}
	}
	_, err = c.callAuthed(ctx, "web.connect", id)
	return err
}

// labels lists the Label plugin's labels. ok is false when the plugin is not
// installed (its methods answer with an RPC error), which is not a failure.
func (c *rpc) labels(ctx context.Context) (labels []string, ok bool, err error) {
	result, err := c.callAuthed(ctx, "label.get_labels")
	if err != nil {
		if _, isRPC := errors.AsType[*rpcError](err); isRPC && !isAuthError(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := json.Unmarshal(result, &labels); err != nil {
		return nil, false, nil
	}
	return labels, true, nil
}

// optional runs one best-effort read of Status: a daemon that answers with an
// RPC error (a method this version lacks) leaves the field at its zero, while
// a login failure, a transport error and a cancelled ctx fail the call.
func (c *rpc) optional(ctx context.Context, into any, method string, params ...any) error {
	result, err := c.callAuthed(ctx, method, params...)
	if err != nil {
		if _, isRPC := errors.AsType[*rpcError](err); isRPC && !isAuthError(err) {
			return nil
		}
		return err
	}
	if json.Unmarshal(result, into) != nil {
		return nil
	}
	return nil
}
