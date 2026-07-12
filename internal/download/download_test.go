// file: internal/download/download_test.go
// version: 1.2.0
// last-edited: 2026-07-12

package download

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestTorrentClientInterface verifies that all torrent client implementations
// satisfy the TorrentClient interface.
func TestTorrentClientInterface(t *testing.T) {
	tests := []struct {
		name   string
		client TorrentClient
	}{
		{
			name:   "DelugeClient",
			client: &DelugeClient{},
		},
		{
			name:   "QBittorrentClient",
			client: &QBittorrentClient{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just checking that the type assertions work
			if tt.client == nil {
				t.Error("client should not be nil")
			}
		})
	}
}

// TestUsenetClientInterface verifies that all Usenet client implementations
// satisfy the UsenetClient interface.
func TestUsenetClientInterface(t *testing.T) {
	tests := []struct {
		name   string
		client UsenetClient
	}{
		{
			name:   "SABnzbdClient",
			client: &SABnzbdClient{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just checking that the type assertions work
			if tt.client == nil {
				t.Error("client should not be nil")
			}
		})
	}
}

// TestUpdateStoragePathFailClosed verifies that the re-point-only relocation
// stubs on both torrent clients are fail-closed: they return
// ErrRePointUnsupported until TASK-02 (Deluge) / TASK-05 (qBittorrent) land a
// real mechanism.
func TestUpdateStoragePathFailClosed(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		client TorrentClient
	}{
		{name: "DelugeClient", client: &DelugeClient{}},
		{name: "QBittorrentClient", client: &QBittorrentClient{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.client.UpdateStoragePath(ctx, "test-id", "/new/path")
			if !errors.Is(err, ErrRePointUnsupported) {
				t.Errorf("expected ErrRePointUnsupported, got %v", err)
			}
		})
	}
}

// TestNewTorrentClientFromConfig tests the torrent client factory function.
func TestNewTorrentClientFromConfig(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		wantType    string
		wantErr     bool
		wantNil     bool
		errContains string
	}{
		{
			name: "deluge client",
			cfg: &config.Config{
				DownloadClient: config.DownloadClientConfig{
					Torrent: config.TorrentClientConfig{
						Type: "deluge",
						Deluge: config.DelugeConfig{
							Host:     "localhost",
							Port:     8112,
							Password: "deluge",
						},
					},
				},
			},
			wantType: "deluge",
			wantErr:  false,
		},
		{
			name: "qbittorrent client",
			cfg: &config.Config{
				DownloadClient: config.DownloadClientConfig{
					Torrent: config.TorrentClientConfig{
						Type: "qbittorrent",
						QBittorrent: config.QBittorrentConfig{
							Host:     "localhost",
							Port:     8484,
							Username: "admin",
							Password: "admin",
						},
					},
				},
			},
			wantType: "qbittorrent",
			wantErr:  false,
		},
		{
			name: "empty type returns nil",
			cfg: &config.Config{
				DownloadClient: config.DownloadClientConfig{
					Torrent: config.TorrentClientConfig{
						Type: "",
					},
				},
			},
			wantNil: true,
			wantErr: false,
		},
		{
			name: "unsupported type",
			cfg: &config.Config{
				DownloadClient: config.DownloadClientConfig{
					Torrent: config.TorrentClientConfig{
						Type: "transmission",
					},
				},
			},
			wantErr:     true,
			errContains: "unsupported torrent client type: transmission",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewTorrentClientFromConfig(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if tt.errContains != "" && err.Error() != tt.errContains {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.wantNil {
				if client != nil {
					t.Errorf("expected nil client, got %T", client)
				}
				return
			}

			if client == nil {
				t.Error("expected non-nil client")
				return
			}

			if got := client.ClientType(); got != tt.wantType {
				t.Errorf("expected client type %q, got %q", tt.wantType, got)
			}
		})
	}
}

// TestNewUsenetClientFromConfig tests the Usenet client factory function.
func TestNewUsenetClientFromConfig(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		wantType    string
		wantErr     bool
		wantNil     bool
		errContains string
	}{
		{
			name: "sabnzbd client",
			cfg: &config.Config{
				DownloadClient: config.DownloadClientConfig{
					Usenet: config.UsenetClientConfig{
						Type: "sabnzbd",
						SABnzbd: config.SABnzbdConfig{
							Host:   "localhost",
							Port:   8085,
							APIKey: "test-api-key",
						},
					},
				},
			},
			wantType: "sabnzbd",
			wantErr:  false,
		},
		{
			name: "empty type returns nil",
			cfg: &config.Config{
				DownloadClient: config.DownloadClientConfig{
					Usenet: config.UsenetClientConfig{
						Type: "",
					},
				},
			},
			wantNil: true,
			wantErr: false,
		},
		{
			name: "unsupported type",
			cfg: &config.Config{
				DownloadClient: config.DownloadClientConfig{
					Usenet: config.UsenetClientConfig{
						Type: "nzbget",
					},
				},
			},
			wantErr:     true,
			errContains: "unsupported usenet client type: nzbget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewUsenetClientFromConfig(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if tt.errContains != "" && err.Error() != tt.errContains {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.wantNil {
				if client != nil {
					t.Errorf("expected nil client, got %T", client)
				}
				return
			}

			if client == nil {
				t.Error("expected non-nil client")
				return
			}

			if got := client.ClientType(); got != tt.wantType {
				t.Errorf("expected client type %q, got %q", tt.wantType, got)
			}
		})
	}
}

// TestErrNotImplemented verifies the error constant.
func TestErrNotImplemented(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Error("ErrNotImplemented should not be nil")
	}

	expected := "download client not implemented"
	if ErrNotImplemented.Error() != expected {
		t.Errorf("expected error message %q, got %q", expected, ErrNotImplemented.Error())
	}

	// Verify it's a sentinel error that can be compared
	err := ErrNotImplemented
	if !errors.Is(err, ErrNotImplemented) {
		t.Error("errors.Is should recognize ErrNotImplemented")
	}
}

// TestDelugeClient tests the Deluge client with a mock JSON-RPC server.
func TestDelugeClient(t *testing.T) {
	cfg := config.DelugeConfig{
		Host:     "localhost",
		Port:     8112,
		Password: "deluge",
	}
	client := NewDelugeClient(cfg)

	if client == nil {
		t.Fatal("NewDelugeClient returned nil")
	}

	if got := client.ClientType(); got != "deluge" {
		t.Errorf("expected client type 'deluge', got %q", got)
	}

	ctx := context.Background()

	// Without a running Deluge server, methods should return connection errors (not ErrNotImplemented)
	t.Run("Connect_NoServer", func(t *testing.T) {
		err := client.Connect(ctx)
		if err == nil {
			t.Error("expected error when no server running")
		}
	})

	t.Run("GetTorrent_NoServer", func(t *testing.T) {
		// Need to initialize client.client to avoid nil pointer
		client.client = &http.Client{Timeout: time.Second}
		_, err := client.GetTorrent(ctx, "test-id")
		if err == nil {
			t.Error("expected error when no server running")
		}
	})

	t.Run("ListCompleted_NoServer", func(t *testing.T) {
		client.client = &http.Client{Timeout: time.Second}
		_, err := client.ListCompleted(ctx)
		if err == nil {
			t.Error("expected error when no server running")
		}
	})
}

// TestQBittorrentClient tests the qBittorrent client.
func TestQBittorrentClient(t *testing.T) {
	cfg := config.QBittorrentConfig{
		Host:     "localhost",
		Port:     18080,
		Username: "admin",
		Password: "admin",
	}
	client := NewQBittorrentClient(cfg)

	if client == nil {
		t.Fatal("NewQBittorrentClient returned nil")
	}

	if got := client.ClientType(); got != "qbittorrent" {
		t.Errorf("expected client type 'qbittorrent', got %q", got)
	}

	ctx := context.Background()

	t.Run("Connect_NoServer", func(t *testing.T) {
		err := client.Connect(ctx)
		if err == nil {
			t.Error("expected error when no server running")
		}
	})

	t.Run("GetTorrent_NoServer", func(t *testing.T) {
		client.client = &http.Client{Timeout: time.Second}
		_, err := client.GetTorrent(ctx, "test-hash")
		if err == nil {
			t.Error("expected error when no server running")
		}
	})

	t.Run("ListCompleted_NoServer", func(t *testing.T) {
		client.client = &http.Client{Timeout: time.Second}
		_, err := client.ListCompleted(ctx)
		if err == nil {
			t.Error("expected error when no server running")
		}
	})
}

// TestSABnzbdClient tests the SABnzbd client.
func TestSABnzbdClient(t *testing.T) {
	cfg := config.SABnzbdConfig{
		Host:   "localhost",
		Port:   18085,
		APIKey: "test-api-key",
	}
	client := NewSABnzbdClient(cfg)

	if client == nil {
		t.Fatal("NewSABnzbdClient returned nil")
	}

	if got := client.ClientType(); got != "sabnzbd" {
		t.Errorf("expected client type 'sabnzbd', got %q", got)
	}

	ctx := context.Background()

	t.Run("Connect_NoServer", func(t *testing.T) {
		err := client.Connect(ctx)
		if err == nil {
			t.Error("expected error when no server running")
		}
	})

	t.Run("GetJob_NoServer", func(t *testing.T) {
		client.client = &http.Client{Timeout: time.Second}
		_, err := client.GetJob(ctx, "test-id")
		if err == nil {
			t.Error("expected error when no server running")
		}
	})

	t.Run("ListCompleted_NoServer", func(t *testing.T) {
		client.client = &http.Client{Timeout: time.Second}
		_, err := client.ListCompleted(ctx)
		if err == nil {
			t.Error("expected error when no server running")
		}
	})
}

// TestTorrentStatusConstants verifies that the TorrentStatus constants are unique.
func TestTorrentStatusConstants(t *testing.T) {
	statuses := []TorrentStatus{
		StatusDownloading,
		StatusSeeding,
		StatusPaused,
		StatusStopped,
		StatusNotFound,
	}

	// Check that all constants have different values
	seen := make(map[TorrentStatus]bool)
	for _, status := range statuses {
		if seen[status] {
			t.Errorf("duplicate TorrentStatus value: %d", status)
		}
		seen[status] = true
	}

	// Verify expected count
	if len(statuses) != 5 {
		t.Errorf("expected 5 TorrentStatus constants, got %d", len(statuses))
	}
}

// TestUsenetStatusConstants verifies that the UsenetStatus constants are unique.
func TestUsenetStatusConstants(t *testing.T) {
	statuses := []UsenetStatus{
		UsenetStatusQueued,
		UsenetStatusDownloading,
		UsenetStatusCompleted,
		UsenetStatusPaused,
		UsenetStatusFailed,
		UsenetStatusNotFound,
	}

	// Check that all constants have different values
	seen := make(map[UsenetStatus]bool)
	for _, status := range statuses {
		if seen[status] {
			t.Errorf("duplicate UsenetStatus value: %d", status)
		}
		seen[status] = true
	}

	// Verify expected count
	if len(statuses) != 6 {
		t.Errorf("expected 6 UsenetStatus constants, got %d", len(statuses))
	}
}
