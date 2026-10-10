// file: internal/deluge/client.go
// version: 1.3.0
// guid: 9a7b8c6d-0e1f-4a70-b8c5-3d7e0f1b9a99
//
// Deluge Web JSON-RPC client (backlog 6.1).
//
// Communicates with Deluge's Web UI at /json using JSON-RPC 2.0.
// Session-based auth via cookie. Supports:
//   - Authentication (auth.login)
//   - Listing torrents (core.get_torrents_status)
//   - Getting single torrent info (core.get_torrent_status)
//   - Moving torrent storage (core.move_storage)
//   - Detail reads (ratio, seed time, files) and removal with data
//
// Reference: https://deluge.readthedocs.io/en/latest/reference/webapi.html
// last-edited: 2026-10-09

package deluge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client talks to a Deluge Web UI instance via JSON-RPC.
type Client struct {
	baseURL  string
	password string
	client   *http.Client
	mu       sync.Mutex
	authed   bool
	reqID    atomic.Int64
}

// TorrentStatus holds the fields we care about from Deluge.
type TorrentStatus struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	SavePath string  `json:"save_path"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	Label    string  `json:"label"`
	// TotalSize is populated when requested via GetTorrent.
	TotalSize int64 `json:"total_size"`
}

// TorrentFile is one entry of Deluge's `files` status field. Path is relative
// to the torrent's save_path.
type TorrentFile struct {
	Index  int    `json:"index"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Offset int64  `json:"offset"`
}

// TorrentDetail is TorrentStatus plus what a cleanup decision needs. The time
// fields are Unix seconds and may be fractional; 0 means unknown.
type TorrentDetail struct {
	TorrentStatus
	Ratio          float64       `json:"ratio"`
	SeedingTime    int64         `json:"seeding_time"` // seconds
	TimeAdded      float64       `json:"time_added"`
	CompletedTime  float64       `json:"completed_time"`
	IsFinished     bool          `json:"is_finished"`
	Files          []TorrentFile `json:"files"`
	FileProgress   []float64     `json:"file_progress"`
	FilePriorities []int         `json:"file_priorities"`
	TotalDone      int64         `json:"total_done"`
}

// AbsPath returns the on-disk path of one of the torrent's files.
func (d TorrentDetail) AbsPath(f TorrentFile) string {
	return filepath.Join(d.SavePath, f.Path)
}

var (
	// ErrTorrentNotFound means Deluge does not know the torrent (already
	// removed), as opposed to Deluge being unreachable.
	ErrTorrentNotFound = errors.New("deluge: torrent not found")
	// ErrRemoveRefused means core.remove_torrent answered false.
	ErrRemoveRefused = errors.New("deluge: remove_torrent refused")
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

type rpcError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// New creates a Deluge Web JSON-RPC client.
// baseURL is the Deluge Web UI URL (e.g. "http://<deluge-host>:8112").
// password is the Web UI password (default: "deluge").
func New(baseURL, password string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:  baseURL,
		password: password,
		client:   &http.Client{Jar: jar, Timeout: 5 * time.Minute},
	}, nil
}

// call sends a JSON-RPC request and decodes the result.
func (c *Client) call(method string, params ...any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	id := c.reqID.Add(1)
	body, _ := json.Marshal(rpcRequest{
		Method: method,
		Params: params,
		ID:     id,
	})

	resp, err := c.client.Post(c.baseURL+"/json", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("deluge rpc %s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode response: %w (body: %s)", err, string(raw[:min(200, len(raw))]))
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("deluge error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// callAuthed is call plus a one-shot recovery from an expired web session:
// on a "Not authenticated" error it clears the cached login, logs in again and
// replays the request once. It lives outside call because Login holds c.mu and
// itself uses call. auth.login is never replayed.
func (c *Client) callAuthed(method string, params ...any) (json.RawMessage, error) {
	result, err := c.call(method, params...)
	if err == nil || method == "auth.login" || !isAuthError(err) {
		return result, err
	}
	c.mu.Lock()
	c.authed = false
	c.mu.Unlock()
	if lerr := c.Login(); lerr != nil {
		return nil, lerr
	}
	return c.call(method, params...)
}

func isAuthError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not authenticated")
}

// isUnknownTorrentError reports an RPC error naming an invalid/unknown torrent.
func isUnknownTorrentError(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "invalid torrent") || strings.Contains(m, "unknown torrent") ||
		strings.Contains(m, "torrent not found") || strings.Contains(m, "invalidtorrenterror")
}

// NormalizeTorrentID lowercases a torrent id and accepts only a 40 (SHA-1) or
// 64 (SHA-256) character hex string, so an empty or malformed id can never
// reach core.remove_torrent.
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

// Login authenticates with the Deluge Web UI. Must be called before
// other methods. Idempotent — skips if already authenticated.
func (c *Client) Login() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authed {
		return nil
	}
	result, err := c.call("auth.login", c.password)
	if err != nil {
		return fmt.Errorf("auth.login: %w", err)
	}
	var ok bool
	if err := json.Unmarshal(result, &ok); err != nil || !ok {
		return fmt.Errorf("auth.login failed (result: %s)", string(result))
	}
	c.authed = true
	return nil
}

// torrentFields is the standard field set requested from Deluge.
// label comes from the Label plugin; Deluge returns "" if it isn't installed
// or if the torrent has no label — both are safe to ignore.
var torrentFields = []string{"hash", "name", "save_path", "state", "progress", "label", "total_size"}

// torrentDetailFields is torrentFields plus the cleanup-decision fields. It is
// built from a copy so the two lists cannot drift.
var torrentDetailFields = append(append([]string{}, torrentFields...),
	"ratio", "seeding_time", "time_added", "completed_time", "is_finished",
	"files", "file_progress", "file_priorities", "total_done")

// ListTorrents returns all torrents with the standard field set.
func (c *Client) ListTorrents() (map[string]TorrentStatus, error) {
	if err := c.Login(); err != nil {
		return nil, err
	}
	result, err := c.callAuthed("core.get_torrents_status", map[string]any{}, torrentFields)
	if err != nil {
		return nil, err
	}
	var torrents map[string]TorrentStatus
	if err := json.Unmarshal(result, &torrents); err != nil {
		return nil, fmt.Errorf("decode torrents: %w", err)
	}
	return torrents, nil
}

// ListTorrentsByLabel returns torrents whose label matches the given string
// (case-insensitive). An empty label returns all torrents.
func (c *Client) ListTorrentsByLabel(label string) ([]TorrentStatus, error) {
	all, err := c.ListTorrents()
	if err != nil {
		return nil, err
	}
	label = strings.ToLower(strings.TrimSpace(label))
	out := make([]TorrentStatus, 0, len(all))
	for _, t := range all {
		if label == "" || strings.ToLower(t.Label) == label {
			out = append(out, t)
		}
	}
	return out, nil
}

// ListLabels returns all labels defined in Deluge's Label plugin.
// Returns an empty slice (not an error) if the Label plugin is not installed.
func (c *Client) ListLabels() ([]string, error) {
	if err := c.Login(); err != nil {
		return nil, err
	}
	result, err := c.callAuthed("label.get_labels")
	if err != nil {
		// Label plugin may not be installed — treat as empty list.
		return []string{}, nil
	}
	var labels []string
	if err := json.Unmarshal(result, &labels); err != nil {
		return []string{}, nil
	}
	return labels, nil
}

// GetTorrent returns status for a single torrent by hash.
func (c *Client) GetTorrent(torrentID string) (*TorrentStatus, error) {
	if err := c.Login(); err != nil {
		return nil, err
	}
	fields := []string{"hash", "name", "save_path", "state", "progress"}
	result, err := c.callAuthed("core.get_torrent_status", torrentID, fields)
	if err != nil {
		return nil, err
	}
	var status TorrentStatus
	if err := json.Unmarshal(result, &status); err != nil {
		return nil, fmt.Errorf("decode torrent: %w", err)
	}
	return &status, nil
}

// MoveStorage moves a torrent's data to a new location on disk.
// This is the key integration point for library centralization —
// when a book version is swapped or reorganized, the torrent's
// storage path needs to follow.
func (c *Client) MoveStorage(torrentIDs []string, destPath string) error {
	if err := c.Login(); err != nil {
		return err
	}
	_, err := c.callAuthed("core.move_storage", torrentIDs, destPath)
	return err
}

// Connected checks whether the Web UI is connected to a daemon.
func (c *Client) Connected() (bool, error) {
	if err := c.Login(); err != nil {
		return false, err
	}
	result, err := c.callAuthed("web.connected")
	if err != nil {
		return false, err
	}
	var connected bool
	_ = json.Unmarshal(result, &connected)
	return connected, nil
}

// GetTorrentDetail returns the detail field set for one torrent. A torrent
// Deluge does not know yields ErrTorrentNotFound.
func (c *Client) GetTorrentDetail(hash string) (*TorrentDetail, error) {
	id, err := NormalizeTorrentID(hash)
	if err != nil {
		return nil, err
	}
	if err := c.Login(); err != nil {
		return nil, err
	}
	result, err := c.callAuthed("core.get_torrent_status", id, torrentDetailFields)
	if err != nil {
		if isUnknownTorrentError(err) {
			return nil, fmt.Errorf("%w: %s", ErrTorrentNotFound, id)
		}
		return nil, err
	}
	var d TorrentDetail
	if err := json.Unmarshal(result, &d); err != nil {
		return nil, fmt.Errorf("decode torrent detail: %w", err)
	}
	if d.Hash == "" {
		return nil, fmt.Errorf("%w: %s", ErrTorrentNotFound, id)
	}
	return &d, nil
}

const detailChunkSize = 50

// ListTorrentDetails fetches detail for the given hashes, at most 50 per
// request, sorted by hash.
func (c *Client) ListTorrentDetails(hashes []string) ([]TorrentDetail, error) {
	ids := make([]string, 0, len(hashes))
	for _, h := range hashes {
		id, err := NormalizeTorrentID(h)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	out := make([]TorrentDetail, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if err := c.Login(); err != nil {
		return nil, err
	}
	for start := 0; start < len(ids); start += detailChunkSize {
		end := min(start+detailChunkSize, len(ids))
		result, err := c.callAuthed("core.get_torrents_status",
			map[string]any{"id": ids[start:end]}, torrentDetailFields)
		if err != nil {
			return nil, err
		}
		var m map[string]TorrentDetail
		if err := json.Unmarshal(result, &m); err != nil {
			return nil, fmt.Errorf("decode torrent details: %w", err)
		}
		for _, d := range m {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out, nil
}

// ListTorrentDetailsByLabel resolves the label with the light listing (same
// case-insensitive semantics as ListTorrentsByLabel) and then fetches detail,
// including the file list, only for the matching torrents.
func (c *Client) ListTorrentDetailsByLabel(label string) ([]TorrentDetail, error) {
	light, err := c.ListTorrentsByLabel(label)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(light))
	for _, t := range light {
		hashes = append(hashes, t.Hash)
	}
	return c.ListTorrentDetails(hashes)
}

// RemoveTorrent removes a torrent, and its downloaded data when removeData is
// true. A false result is returned as ErrRemoveRefused so a caller that ignores
// the bool still fails closed. It has no production caller until the cleanup
// fixer lands.
func (c *Client) RemoveTorrent(hash string, removeData bool) (bool, error) {
	id, err := NormalizeTorrentID(hash)
	if err != nil {
		return false, err
	}
	if err := c.Login(); err != nil {
		return false, err
	}
	result, err := c.callAuthed("core.remove_torrent", id, removeData)
	if err != nil {
		if isUnknownTorrentError(err) {
			return false, fmt.Errorf("%w: %s", ErrTorrentNotFound, id)
		}
		return false, fmt.Errorf("remove torrent %s: %w", id, err)
	}
	var ok bool
	if err := json.Unmarshal(result, &ok); err != nil {
		return false, fmt.Errorf("remove torrent %s: decode result %s: %w", id, string(result), err)
	}
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrRemoveRefused, id)
	}
	return true, nil
}
