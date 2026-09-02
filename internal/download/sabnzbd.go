// file: internal/download/sabnzbd.go
// version: 1.2.1
// guid: 2670e805-a4a5-4cd0-870a-fe15f09bd4e8
// last-edited: 2026-09-02

package download

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// SABnzbdClient implements UsenetClient for SABnzbd via its REST API.
type SABnzbdClient struct {
	cfg     config.SABnzbdConfig
	client  *http.Client
	baseURL string
}

// NewSABnzbdClient constructs a SABnzbd client adapter.
func NewSABnzbdClient(cfg config.SABnzbdConfig) *SABnzbdClient {
	scheme := "http"
	if cfg.UseHTTPS {
		scheme = "https"
	}
	return &SABnzbdClient{
		cfg:     cfg,
		baseURL: fmt.Sprintf("%s://%s:%d/api", scheme, cfg.Host, cfg.Port),
	}
}

func (s *SABnzbdClient) apiCall(ctx context.Context, mode string, extra url.Values) (jsontext.Value, error) {
	params := url.Values{
		"apikey": {s.cfg.APIKey},
		"output": {"json"},
		"mode":   {mode},
	}
	maps.Copy(params, extra)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sabnzbd: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sabnzbd: API returned status %d", resp.StatusCode)
	}

	var raw jsontext.Value
	if err := json.UnmarshalRead(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("sabnzbd: failed to parse response: %w", err)
	}
	return raw, nil
}

// Connect validates credentials and connectivity for SABnzbd.
func (s *SABnzbdClient) Connect(ctx context.Context) error {
	s.client = &http.Client{Timeout: 30 * time.Second}

	result, err := s.apiCall(ctx, "version", nil)
	if err != nil {
		return fmt.Errorf("sabnzbd: connection failed: %w", err)
	}

	// Check for API error response
	var errResp struct {
		Status bool   `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(result, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("sabnzbd: API error: %s", errResp.Error)
	}
	return nil
}

type sabSlot struct {
	NzoID      string `json:"nzo_id"`
	Filename   string `json:"filename"`
	Storage    string `json:"storage"`
	Status     string `json:"status"`
	Percentage string `json:"percentage"`
	Bytes      string `json:"bytes"`
	Size       string `json:"size"`
}

func mapSABStatus(status string) UsenetStatus {
	switch status {
	case "Queued":
		return UsenetStatusQueued
	case "Downloading", "Extracting", "Verifying", "Repairing":
		return UsenetStatusDownloading
	case "Completed":
		return UsenetStatusCompleted
	case "Paused":
		return UsenetStatusPaused
	case "Failed":
		return UsenetStatusFailed
	default:
		return UsenetStatusQueued
	}
}

func sabSlotToNZBInfo(slot sabSlot) NZBInfo {
	var progress float64
	fmt.Sscanf(slot.Percentage, "%f", &progress)
	return NZBInfo{
		ID:          slot.NzoID,
		Name:        slot.Filename,
		DownloadDir: slot.Storage,
		Status:      mapSABStatus(slot.Status),
		Progress:    progress / 100.0,
		IsPaused:    slot.Status == "Paused",
	}
}

// GetJob returns detailed info for a single Usenet job.
func (s *SABnzbdClient) GetJob(ctx context.Context, id string) (*NZBInfo, error) {
	// Check queue first
	result, err := s.apiCall(ctx, "queue", url.Values{"nzo_ids": {id}})
	if err != nil {
		return nil, err
	}

	var queueResp struct {
		Queue struct {
			Slots []sabSlot `json:"slots"`
		} `json:"queue"`
	}
	if json.Unmarshal(result, &queueResp) == nil {
		for _, slot := range queueResp.Queue.Slots {
			if slot.NzoID == id {
				info := sabSlotToNZBInfo(slot)
				return &info, nil
			}
		}
	}

	// Check history
	result, err = s.apiCall(ctx, "history", url.Values{"nzo_ids": {id}})
	if err != nil {
		return nil, err
	}

	var histResp struct {
		History struct {
			Slots []sabSlot `json:"slots"`
		} `json:"history"`
	}
	if json.Unmarshal(result, &histResp) == nil {
		for _, slot := range histResp.History.Slots {
			if slot.NzoID == id {
				info := sabSlotToNZBInfo(slot)
				return &info, nil
			}
		}
	}

	return nil, nil // not found
}

// GetQueueStats returns lightweight stats for a job.
func (s *SABnzbdClient) GetQueueStats(ctx context.Context, id string) (*UsenetStats, error) {
	info, err := s.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return &UsenetStats{Exists: false}, nil
	}
	return &UsenetStats{
		IsPaused: info.IsPaused,
		Exists:   true,
	}, nil
}

// SetDownloadPath changes the download category/path for a job.
func (s *SABnzbdClient) SetDownloadPath(ctx context.Context, id, newPath string) error {
	_, err := s.apiCall(ctx, "change_complete_action", url.Values{
		"value":  {id},
		"value2": {newPath},
	})
	return err
}

// RemoveJob removes a Usenet job from SABnzbd.
func (s *SABnzbdClient) RemoveJob(ctx context.Context, id string, deleteFiles bool) error {
	mode := "queue"
	extra := url.Values{"name": {"delete"}, "value": {id}}
	if deleteFiles {
		extra.Set("del_files", "1")
	}

	// Try removing from queue first
	if _, err := s.apiCall(ctx, mode, extra); err != nil {
		// Try history
		extra.Set("name", "delete")
		extra.Set("value", id)
		_, err = s.apiCall(ctx, "history", extra)
		return err
	}
	return nil
}

// ListCompleted returns all completed Usenet jobs.
func (s *SABnzbdClient) ListCompleted(ctx context.Context) ([]NZBInfo, error) {
	result, err := s.apiCall(ctx, "history", url.Values{"limit": {"100"}})
	if err != nil {
		return nil, err
	}

	var histResp struct {
		History struct {
			Slots []sabSlot `json:"slots"`
		} `json:"history"`
	}
	if err := json.Unmarshal(result, &histResp); err != nil {
		return nil, fmt.Errorf("sabnzbd: failed to parse history: %w", err)
	}

	var completed []NZBInfo
	for _, slot := range histResp.History.Slots {
		if slot.Status == "Completed" {
			completed = append(completed, sabSlotToNZBInfo(slot))
		}
	}
	return completed, nil
}

// ClientType returns the identifier for this client.
func (s *SABnzbdClient) ClientType() string {
	return "sabnzbd"
}
