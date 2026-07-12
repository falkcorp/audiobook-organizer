// file: internal/download/deluge.go
// version: 1.2.0
// guid: 466129e8-037a-4da5-a961-078808151e0e
// last-edited: 2026-07-12

package download

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// DelugeClient implements TorrentClient for Deluge via JSON-RPC.
type DelugeClient struct {
	cfg     config.DelugeConfig
	client  *http.Client
	baseURL string
	rpcID   atomic.Int64
}

// NewDelugeClient constructs a Deluge client adapter.
func NewDelugeClient(cfg config.DelugeConfig) *DelugeClient {
	return &DelugeClient{
		cfg:     cfg,
		baseURL: fmt.Sprintf("http://%s:%d/json", cfg.Host, cfg.Port),
	}
}

type delugeRPCRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int64  `json:"id"`
}

type delugeRPCResponse struct {
	Result jsontext.Value `json:"result"`
	Error  *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
	ID int64 `json:"id"`
}

func (d *DelugeClient) call(ctx context.Context, method string, params ...any) (jsontext.Value, error) {
	id := d.rpcID.Add(1)
	body, _ := json.Marshal(delugeRPCRequest{Method: method, Params: params, ID: id})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deluge: request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp delugeRPCResponse
	if err := json.UnmarshalRead(resp.Body, &rpcResp); err != nil {
		return nil, fmt.Errorf("deluge: failed to parse response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("deluge: RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// Connect authenticates with the Deluge Web UI.
func (d *DelugeClient) Connect(ctx context.Context) error {
	jar, _ := cookiejar.New(nil)
	d.client = &http.Client{Jar: jar, Timeout: 30 * time.Second}

	// Login to web UI
	result, err := d.call(ctx, "auth.login", d.cfg.Password)
	if err != nil {
		return fmt.Errorf("deluge: login failed: %w", err)
	}
	var success bool
	if err := json.Unmarshal(result, &success); err != nil || !success {
		return fmt.Errorf("deluge: authentication rejected")
	}

	// Connect to the first available daemon
	_, err = d.call(ctx, "web.connected")
	if err != nil {
		return fmt.Errorf("deluge: connection check failed: %w", err)
	}
	return nil
}

type delugeTorrent struct {
	Name          string  `json:"name"`
	SavePath      string  `json:"save_path"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	TotalUploaded int64   `json:"total_uploaded"`
	TotalSize     int64   `json:"total_size"`
	TimeAdded     float64 `json:"time_added"`
	Paused        bool    `json:"paused"`
	Files         []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files"`
}

func mapDelugeState(state string, paused bool) TorrentStatus {
	if paused {
		return StatusPaused
	}
	switch state {
	case "Downloading", "Checking":
		return StatusDownloading
	case "Seeding":
		return StatusSeeding
	case "Paused":
		return StatusPaused
	default:
		return StatusStopped
	}
}

func delugeToTorrentInfo(id string, t delugeTorrent) TorrentInfo {
	info := TorrentInfo{
		ID:              id,
		Name:            t.Name,
		DownloadDir:     t.SavePath,
		Status:          mapDelugeState(t.State, t.Paused),
		Progress:        t.Progress / 100.0,
		TotalUploaded:   t.TotalUploaded,
		TotalDownloaded: t.TotalSize,
		CreatedAt:       time.Unix(int64(t.TimeAdded), 0),
		IsPaused:        t.Paused,
	}
	for _, f := range t.Files {
		info.Files = append(info.Files, TorrentFile{Path: f.Path, Size: f.Size})
	}
	return info
}

var delugeFields = []string{
	"name", "save_path", "state", "progress", "total_uploaded",
	"total_size", "time_added", "paused", "files",
}

// GetTorrent returns detailed info for a single torrent.
func (d *DelugeClient) GetTorrent(ctx context.Context, id string) (*TorrentInfo, error) {
	result, err := d.call(ctx, "web.get_torrent_status", id, delugeFields)
	if err != nil {
		return nil, err
	}
	var t delugeTorrent
	if err := json.Unmarshal(result, &t); err != nil {
		return nil, fmt.Errorf("deluge: failed to parse torrent: %w", err)
	}
	if t.Name == "" {
		return nil, nil
	}
	info := delugeToTorrentInfo(id, t)
	return &info, nil
}

// GetUploadStats returns lightweight stats for cleanup monitoring.
func (d *DelugeClient) GetUploadStats(ctx context.Context, id string) (*UploadStats, error) {
	result, err := d.call(ctx, "web.get_torrent_status", id, []string{"total_uploaded", "paused"})
	if err != nil {
		return nil, err
	}
	var data struct {
		TotalUploaded int64 `json:"total_uploaded"`
		Paused        bool  `json:"paused"`
	}
	if err := json.Unmarshal(result, &data); err != nil {
		return &UploadStats{Exists: false}, nil
	}
	return &UploadStats{
		TotalUploaded: data.TotalUploaded,
		IsPaused:      data.Paused,
		Exists:        true,
	}, nil
}

// SetDownloadPath performs a PHYSICAL move of a torrent's data to a new
// download directory (Deluge core.move_storage). For re-point-only relocation
// where the caller has already moved the data, use UpdateStoragePath instead.
func (d *DelugeClient) SetDownloadPath(ctx context.Context, id, newPath string) error {
	_, err := d.call(ctx, "core.set_torrent_move_completed_path", id, newPath)
	if err != nil {
		return err
	}
	_, err = d.call(ctx, "core.move_storage", []string{id}, newPath)
	return err
}

// UpdateStoragePath is the re-point-only relocation stub. It is fail-closed:
// until the real-Deluge spike (TASK-02) lands a confirmed mechanism, it returns
// ErrRePointUnsupported so callers never assume a path change that did not
// happen.
func (d *DelugeClient) UpdateStoragePath(ctx context.Context, id, newPath string) error {
	return ErrRePointUnsupported
}

// RemoveTorrent removes a torrent from the client.
func (d *DelugeClient) RemoveTorrent(ctx context.Context, id string, deleteFiles bool) error {
	_, err := d.call(ctx, "core.remove_torrent", id, deleteFiles)
	return err
}

// ListCompleted returns all completed torrents.
func (d *DelugeClient) ListCompleted(ctx context.Context) ([]TorrentInfo, error) {
	filterFields := []string{"name", "save_path", "state", "progress", "total_uploaded", "total_size", "time_added", "paused"}
	result, err := d.call(ctx, "web.update_ui", filterFields, map[string]any{})
	if err != nil {
		return nil, err
	}

	var ui struct {
		Torrents map[string]delugeTorrent `json:"torrents"`
	}
	if err := json.Unmarshal(result, &ui); err != nil {
		return nil, fmt.Errorf("deluge: failed to parse torrent list: %w", err)
	}

	var completed []TorrentInfo
	for hash, t := range ui.Torrents {
		if t.Progress >= 100.0 {
			completed = append(completed, delugeToTorrentInfo(hash, t))
		}
	}
	return completed, nil
}

// ClientType returns the identifier for this client.
func (d *DelugeClient) ClientType() string {
	return "deluge"
}
