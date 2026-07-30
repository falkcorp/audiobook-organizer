// file: internal/server/handlers/abs/dto.go
// version: 1.0.0
// guid: 4a8f30c7-1b56-49e2-8d70-63c1e9b28a5f
// last-edited: 2026-07-30

package abs

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The DTOs in this file are a TYPE CONTRACT, not a convenience. Both target clients
// use strict decoders: AudioBooth's Swift Codable is a single all-or-nothing
// `try decoder.decode(T.self)` with no per-element `try?`, so ONE wrong field type
// throws and the whole response is lost; Absorb's Dart casts throw inside widget
// build() and red-screen the UI. Every choice below traces to a source-audited
// requirement in design spec §1.6–1.9, and each is asserted by a test.
//
// The invariants, in order of how much damage they do when violated:
//
//  1. Every Date-ish field is an INTEGER MILLISECOND EPOCH (§1.8.5 item 1).
//     ISO-8601 strings are fatal; so is a float with a fractional part.
//  2. `mediaProgress` is the COMPLETE list, never paginated, never null (§1.8.1).
//     AudioBooth DELETES local progress rows absent from it. This is the single most
//     dangerous field in the project.
//  3. `userDefaultLibraryId` is a non-null String (§1.8.2) or AudioBooth cannot log
//     in at all.
//  4. Booleans are real JSON booleans (§1.8.5 item 10): 0/1 throws in Swift and
//     reads as false in Dart.
//  5. Counts are integers, never floats (§1.7.3 item 5).

// msEpoch converts a time to the integer millisecond epoch every client expects.
// The zero time becomes 0 rather than a negative epoch.
func msEpoch(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// strPtr returns nil for an empty string so the field marshals as JSON null, matching
// what real ABS returns for an unset email.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// userPermissions is the ABS permission block. §1.8.2 and §1.7.3 item 6 make
// update/delete/download required and require real booleans throughout.
type userPermissions struct {
	AccessAllLibraries        bool `json:"accessAllLibraries"`
	AccessAllTags             bool `json:"accessAllTags"`
	AccessExplicitContent     bool `json:"accessExplicitContent"`
	CreateEreader             bool `json:"createEreader"`
	Delete                    bool `json:"delete"`
	Download                  bool `json:"download"`
	SelectedTagsNotAccessible bool `json:"selectedTagsNotAccessible"`
	Update                    bool `json:"update"`
	Upload                    bool `json:"upload"`
}

// userDTO is the ABS `user` object.
//
// Token placement is load-bearing (§3.1 / §1.7.2): clients read user.accessToken,
// user.refreshToken and the legacy user.token — NOT top-level fields. refreshToken is
// always emitted, because omitting it sets Absorb's isLegacy flag and disables
// refresh permanently for that server.
type userDTO struct {
	// AccessToken / RefreshToken are omitted on GET /api/me, which real ABS answers
	// with only the legacy `token`.
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	Token        string `json:"token"`

	// Bookmarks and MediaProgress are always non-nil arrays. See invariant 2 above:
	// a null or truncated MediaProgress destroys the user's listening positions.
	Bookmarks     []any `json:"bookmarks"`
	MediaProgress []any `json:"mediaProgress"`

	CreatedAt        int64    `json:"createdAt"`
	Email            *string  `json:"email"`
	HasOpenIDLink    bool     `json:"hasOpenIDLink"`
	ID               string   `json:"id"`
	IsActive         bool     `json:"isActive"`
	IsLocked         bool     `json:"isLocked"`
	ItemTagsSelected []string `json:"itemTagsSelected"`
	// LastSeen is a *int64 so it can marshal as JSON null. It is always null today:
	// database.User has no last-seen column, and real ABS returns null here too. The
	// pointer type is what keeps that honest instead of emitting a misleading 0.
	LastSeen                        *int64          `json:"lastSeen"`
	LibrariesAccessible             []string        `json:"librariesAccessible"`
	Permissions                     userPermissions `json:"permissions"`
	SeriesHideFromContinueListening []string        `json:"seriesHideFromContinueListening"`
	Type                            string          `json:"type"`
	Username                        string          `json:"username"`
}

// ereaderDevice is an entry of the ereaderDevices array. §1.8.2 notes
// ereaderDevices[].name is non-optional, so an entry always carries a name; we emit
// an empty array, which is valid and avoids the question entirely.
type ereaderDevice struct {
	Name string `json:"name"`
}

// serverSettings mirrors ABS 2.36.0's settings object field-for-field.
//
// It is verbose on purpose: AudioBooth decodes serverSettings.{id, version,
// sortingIgnorePrefix} non-optionally (§1.8.2), and because the decode is
// all-or-nothing there is no benefit to emitting a subset — a field we omit that some
// client decodes non-optionally is an outage. Values are the real defaults captured
// from the 2.36.0 oracle; only `version` is dynamic.
type serverSettings struct {
	AllowIframe                     bool     `json:"allowIframe"`
	AllowedOrigins                  []string `json:"allowedOrigins"`
	AuthActiveAuthMethods           []string `json:"authActiveAuthMethods"`
	AuthLoginCustomMessage          *string  `json:"authLoginCustomMessage"`
	AuthOpenIDAuthorizationURL      *string  `json:"authOpenIDAuthorizationURL"`
	AuthOpenIDAutoLaunch            bool     `json:"authOpenIDAutoLaunch"`
	AuthOpenIDAutoRegister          bool     `json:"authOpenIDAutoRegister"`
	AuthOpenIDButtonText            string   `json:"authOpenIDButtonText"`
	AuthOpenIDIssuerURL             *string  `json:"authOpenIDIssuerURL"`
	AuthOpenIDJwksURL               *string  `json:"authOpenIDJwksURL"`
	AuthOpenIDLogoutURL             *string  `json:"authOpenIDLogoutURL"`
	AuthOpenIDMatchExistingBy       *string  `json:"authOpenIDMatchExistingBy"`
	AuthOpenIDTokenSigningAlgorithm string   `json:"authOpenIDTokenSigningAlgorithm"`
	AuthOpenIDTokenURL              *string  `json:"authOpenIDTokenURL"`
	AuthOpenIDUserInfoURL           *string  `json:"authOpenIDUserInfoURL"`
	BackupPath                      string   `json:"backupPath"`
	BackupSchedule                  bool     `json:"backupSchedule"`
	BackupsToKeep                   int      `json:"backupsToKeep"`
	BookshelfView                   int      `json:"bookshelfView"`
	BuildNumber                     int      `json:"buildNumber"`
	ChromecastEnabled               bool     `json:"chromecastEnabled"`
	DateFormat                      string   `json:"dateFormat"`
	HomeBookshelfView               int      `json:"homeBookshelfView"`
	ID                              string   `json:"id"`
	Language                        string   `json:"language"`
	LogLevel                        int      `json:"logLevel"`
	LoggerDailyLogsToKeep           int      `json:"loggerDailyLogsToKeep"`
	LoggerScannerLogsToKeep         int      `json:"loggerScannerLogsToKeep"`
	MaxBackupSize                   int      `json:"maxBackupSize"`
	MetadataFileFormat              string   `json:"metadataFileFormat"`
	PodcastEpisodeSchedule          string   `json:"podcastEpisodeSchedule"`
	RateLimitLoginRequests          int      `json:"rateLimitLoginRequests"`
	RateLimitLoginWindow            int64    `json:"rateLimitLoginWindow"`
	ScannerCoverProvider            string   `json:"scannerCoverProvider"`
	ScannerDisableWatcher           bool     `json:"scannerDisableWatcher"`
	ScannerFindCovers               bool     `json:"scannerFindCovers"`
	ScannerParseSubtitle            bool     `json:"scannerParseSubtitle"`
	ScannerPreferMatchedMetadata    bool     `json:"scannerPreferMatchedMetadata"`
	SortingIgnorePrefix             bool     `json:"sortingIgnorePrefix"`
	SortingPrefixes                 []string `json:"sortingPrefixes"`
	StoreCoverWithItem              bool     `json:"storeCoverWithItem"`
	StoreMetadataWithItem           bool     `json:"storeMetadataWithItem"`
	TimeFormat                      string   `json:"timeFormat"`
	TimeZone                        string   `json:"timeZone"`
	Version                         string   `json:"version"`
}

// authResponse is the body of POST /login and POST /auth/refresh. Identical in both
// credential modes so no client can tell Mode B from Mode C (§3.0.1).
type authResponse struct {
	Source               string          `json:"Source"`
	EreaderDevices       []ereaderDevice `json:"ereaderDevices"`
	ServerSettings       serverSettings  `json:"serverSettings"`
	User                 userDTO         `json:"user"`
	UserDefaultLibraryID string          `json:"userDefaultLibraryId"`
}

// statusResponse is GET /status.
type statusResponse struct {
	App           string       `json:"app"`
	AuthFormData  authFormData `json:"authFormData"`
	AuthMethods   []string     `json:"authMethods"`
	IsInit        bool         `json:"isInit"`
	Language      string       `json:"language"`
	ServerVersion string       `json:"serverVersion"`
}

type authFormData struct {
	AuthLoginCustomMessage string `json:"authLoginCustomMessage"`
}

// sessionDTO is one entry of GET /api/me/sessions. createdAt/updatedAt are integer ms
// epochs and `current` is a real boolean.
type sessionDTO struct {
	CreatedAt  int64  `json:"createdAt"`
	Current    bool   `json:"current"`
	DeviceInfo any    `json:"deviceInfo"`
	ID         string `json:"id"`
	IPAddress  string `json:"ipAddress"`
	UpdatedAt  int64  `json:"updatedAt"`
	UserAgent  string `json:"userAgent"`
}

// sessionsResponse is GET /api/me/sessions. total/numPages/page/itemsPerPage are all
// integers (§1.7.3 item 5: Dart throws on `42.0 as int?`).
type sessionsResponse struct {
	ItemsPerPage int          `json:"itemsPerPage"`
	NumPages     int          `json:"numPages"`
	Page         int          `json:"page"`
	Sessions     []sessionDTO `json:"sessions"`
	Total        int          `json:"total"`
}

// buildServerSettings renders the settings block for the configured version.
func (h *Handler) buildServerSettings() serverSettings {
	return serverSettings{
		AllowIframe:                     false,
		AllowedOrigins:                  []string{},
		AuthActiveAuthMethods:           []string{"local"},
		AuthOpenIDButtonText:            "Login with OpenId",
		AuthOpenIDTokenSigningAlgorithm: "RS256",
		BackupPath:                      "/metadata/backups",
		BackupsToKeep:                   2,
		BookshelfView:                   1,
		BuildNumber:                     1,
		DateFormat:                      "MM/dd/yyyy",
		HomeBookshelfView:               1,
		ID:                              "server-settings",
		Language:                        "en-us",
		LogLevel:                        2,
		LoggerDailyLogsToKeep:           7,
		LoggerScannerLogsToKeep:         2,
		MaxBackupSize:                   1,
		MetadataFileFormat:              "json",
		PodcastEpisodeSchedule:          "0 * * * *",
		// Advertised login limits. They describe our own throttle so a client can
		// pace itself instead of discovering the limit by getting 429'd.
		RateLimitLoginRequests: 10,
		RateLimitLoginWindow:   600000,
		ScannerCoverProvider:   "google",
		SortingPrefixes:        []string{"the", "a"},
		TimeFormat:             "HH:mm",
		TimeZone:               "UTC",
		Version:                h.cfg.ServerVersion,
	}
}

// buildUser renders the ABS user object.
//
// tokens are threaded in rather than minted here so /api/me can emit only the legacy
// `token` while /login and /auth/refresh emit all three.
func (h *Handler) buildUser(user *database.User, accessToken, refreshToken string, progress, bookmarks []any) userDTO {
	// Non-nil slices only: a null mediaProgress or bookmarks array fails the Swift
	// decode, and a null mediaProgress in particular is the §1.8.1 data-loss case.
	if progress == nil {
		progress = []any{}
	}
	if bookmarks == nil {
		bookmarks = []any{}
	}
	return userDTO{
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		Token:         accessToken,
		Bookmarks:     bookmarks,
		MediaProgress: progress,
		CreatedAt:     msEpoch(user.CreatedAt),
		Email:         strPtr(user.Email),
		HasOpenIDLink: false,
		ID:            user.ID,
		IsActive:      isActiveUser(user),
		IsLocked:      !isActiveUser(user),
		// Empty rather than absent: the keys are decoded non-optionally.
		ItemTagsSelected:                []string{},
		LastSeen:                        nil,
		LibrariesAccessible:             []string{},
		Permissions:                     defaultPermissions(),
		SeriesHideFromContinueListening: []string{},
		// §1.7.4, the cheapest win in the whole spec: reporting "user" rather than
		// "root" makes Absorb hide the ENTIRE admin UI, none of which we implement.
		Type:     "user",
		Username: user.Username,
	}
}

// defaultPermissions grants the read/play/progress permissions the ABS surface
// actually serves. update/delete/download must be present and true or the clients
// disable working features; nothing here grants access to the management API, which
// stays on /api/v1 behind its own permission checks (spec §3.6 router split).
func defaultPermissions() userPermissions {
	return userPermissions{
		AccessAllLibraries:        true,
		AccessAllTags:             true,
		AccessExplicitContent:     true,
		CreateEreader:             false,
		Delete:                    true,
		Download:                  true,
		SelectedTagsNotAccessible: false,
		Update:                    true,
		Upload:                    false,
	}
}
