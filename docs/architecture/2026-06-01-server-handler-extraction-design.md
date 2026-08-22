<!-- file: docs/architecture/2026-06-01-server-handler-extraction-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 89456123-acec-4d17-85c0-0c41a4639bb4 -->
<!-- last-edited: 2026-08-22 -->

# Server Handler Extraction — ADR-003 Phase 2/3 Design

**Date:** 2026-06-01  
**Status:** Approved for implementation  
**Author:** Claude (brainstorming session)

---

## 1. Goal

Break the `*Server` receiver dependency in `internal/server/*_handlers.go` so that:

1. **Testability**: handler logic can be tested with minimal mocks, no full `Server` construction required
2. **Clarity**: a handler group's dependency surface is explicit in its type signature — no grepping needed

Build time is not a primary goal.

---

## 2. Scope

24 handler files (~15K lines, 270 handler methods) migrated in 4 phases. Phase 1 establishes the pattern; phases 2–4 apply it mechanically.

The `internal/server/handlers/` package from ADR-003 Phase 1 (request/response types) is the foundation.

---

## 3. Core Pattern

### 3.1 Handler group struct

Every handler domain becomes a struct in its package. The struct holds only what it needs, declared as narrow interfaces (not concrete types).

```go
// internal/server/handlers/apikeys.go  (already has type defs — add below them)

// Store is the narrow database interface apikeys handlers require.
type APIKeyStore interface {
    CreateAPIKey(key *database.APIKey) (*database.APIKey, error)
    GetAPIKey(id int) (*database.APIKey, error)
    GetUserByID(id string) (*database.User, error)
    ListAllAPIKeys() ([]database.APIKey, error)
    ListAPIKeysForUser(userID string) ([]database.APIKey, error)
    RevokeAPIKey(id int) error
    SetAPIKeyStatus(id int, status string, at time.Time) error
}

// APIKeyHandler handles all /auth/api-keys routes.
type APIKeyHandler struct {
    store APIKeyStore
}

func NewAPIKeyHandler(store APIKeyStore) *APIKeyHandler {
    return &APIKeyHandler{store: store}
}

// Create handles POST /auth/api-keys
func (h *APIKeyHandler) Create(c *gin.Context) { ... }
// List handles GET /auth/api-keys
func (h *APIKeyHandler) List(c *gin.Context) { ... }
// ... remaining methods
```

### 3.2 Narrow interfaces — rules

- **One interface per external dependency type** (store, service, cache)
- **Only methods actually called** — grep the handler file, add exactly those methods
- **Named for the handler group**, not the dependency: `APIKeyStore`, not `StoreForAPIKeys`
- **Live in the same file as the handler struct** — interface + handler struct + methods in one file
- **For services**: if a handler uses `s.audiobookService`, the interface declares only the methods called: `type AudiobookService interface { ListBooks(...) ... }`
- **For caches**: accept `*cache.Cache[T]` directly (already a clean generic type — no wrapper needed)

### 3.3 Handling package-level state

`auth_handlers.go` has package-level lockout state (`loginLockout` map, `isLockedOut`, `recordFailedLogin`, `clearFailedLogins`). This moves onto the `AuthHandler` struct:

```go
type AuthHandler struct {
    store        AuthStore
    lockout      map[string]*failedAttempt   // was package-level
    lockoutMu    sync.Mutex
}

func NewAuthHandler(store AuthStore) *AuthHandler {
    return &AuthHandler{
        store:   store,
        lockout: make(map[string]*failedAttempt),
    }
}
```

This is the correct fix — package-level state is untestable (tests share state across parallel runs).

### 3.4 Helper / utility functions

File-local helper functions (e.g., `buildAPIKeyResponse`, `buildAuthUserResponse`, `isAdminUser`) move to the same file as the handler struct. They become package-level functions (not methods) unless they need the handler's fields.

---

## 4. Package Structure

### 4.1 Small domains — structs added to existing `handlers/` files

The 15 smaller handler domains add their `FooHandler` struct and narrow interfaces directly into the existing `internal/server/handlers/*.go` type files from Phase 1. Each file becomes: types → narrow interfaces → handler struct → methods.

| Handler file (source) | handlers/ file (destination) | Handler struct name |
|---|---|---|
| `auth_handlers.go` | `handlers/auth.go` | `AuthHandler` |
| `apikey_handlers.go` | `handlers/apikeys.go` | `APIKeyHandler` |
| `cache_handlers.go` | `handlers/cache.go` | `CacheHandler` |
| `reading_handlers.go` | `handlers/reading.go` | `ReadingHandler` |
| `playlist_handlers.go` | `handlers/playlists.go` | `PlaylistHandler` |
| `itunes_handlers.go` | `handlers/itunes.go` | `ITunesHandler` |
| `metadata_cached_handlers.go` | `handlers/metadata_cache.go` (new) | `MetadataCacheHandler` |
| `organize_handlers.go` | `handlers/organize.go` (new) | `OrganizeHandler` |
| `filesystem_handlers.go` | `handlers/filesystem.go` (new) | `FilesystemHandler` |
| `split_book_handlers.go` | `handlers/split_book.go` (new) | `SplitBookHandler` |
| `user_handlers.go` | `handlers/user.go` (new) | `UserHandler` |
| `plugins_handlers.go` | `handlers/plugins.go` (new) | `PluginsHandler` |
| `activity_handlers.go` | `handlers/activity.go` (new) | `ActivityHandler` |
| `ai_handlers.go` | `handlers/ai.go` (new) | `AIHandler` |
| `diagnostics_handlers.go` | `handlers/diagnostics.go` (new) | `DiagnosticsHandler` |

### 4.2 Large domains — own sub-packages

The 7 large domains each get `internal/server/handlers/<domain>/` with two files:

```
internal/server/handlers/
├── audiobooks/
│   ├── handler.go      # Handler struct, New(), all methods
│   └── interfaces.go   # AudiobookStore, AudiobookService, etc. narrow interfaces
├── metadata/
│   ├── handler.go
│   └── interfaces.go
├── entities/
│   ├── handler.go
│   └── interfaces.go
├── dedup/
│   ├── handler.go
│   └── interfaces.go
├── duplicates/
│   ├── handler.go
│   └── interfaces.go
├── operations/
│   ├── handler.go
│   └── interfaces.go
└── system/
    ├── handler.go
    └── interfaces.go
```

Sub-package criteria: ≥20 methods OR ≥950 lines.

| Domain | Methods | Lines | Notes |
|---|---|---|---|
| `audiobooks` | 39 | 1,844 | Also uses listCache, facetsCache |
| `entities` | 35 | 1,022 | Also uses authorsCache, seriesCache |
| `metadata` | 26 | 1,954 | Also uses listCache, opRegistry |
| `operations` | 25 | 737 | Also uses opRegistry, scheduler |
| `system` | 24 | 738 | Also uses systemService |
| `dedup` | 21 | 1,325 | Also uses dedupEngine, activityWriter |
| `duplicates` | 19 | 931 | Also uses mergeService, workService |

---

## 5. Route Registration

### 5.1 New `wire_handlers.go`

All handler instantiation and route registration moves from `server_lifecycle.go` into a new file `internal/server/wire_handlers.go`. This keeps lifecycle concerns (startup, shutdown, TLS, workers) separate from HTTP routing.

```go
// internal/server/wire_handlers.go

func (s *Server) wireHandlers() {
    // Instantiate handler structs
    authHandler    := handlers.NewAuthHandler(s.Store())
    apiKeyHandler  := handlers.NewAPIKeyHandler(s.Store())
    audiobooksH    := audiobooks.New(s.Store(), s.audiobookService, s.listCache, s.facetsCache)
    // ...

    // Register routes (exact same route paths, just different handler references)
    authGroup := s.router.Group("/api/v1/auth")
    authGroup.GET("/status", authHandler.GetStatus)
    authGroup.POST("/login",  authHandler.Login)
    // ...
}
```

`server_lifecycle.go` calls `s.wireHandlers()` where route registration currently lives.

### 5.2 Old `*Server` methods

After a handler group is migrated:
- The `*Server` methods are **deleted** (no delegation wrappers — they're just noise)
- Route registration in `server_lifecycle.go` is replaced by `s.wireHandlers()` call
- The old handler file (`auth_handlers.go`) is **deleted** once all its methods are in `handlers/`

---

## 6. Test Pattern

Each handler group gets a `_test.go` file alongside it. Tests use hand-rolled minimal mocks — not the full `database/mocks/MockStore`.

```go
// internal/server/handlers/apikeys_test.go

package handlers_test

// mockAPIKeyStore implements only the 7 methods APIKeyHandler uses.
type mockAPIKeyStore struct {
    createAPIKey       func(key *database.APIKey) (*database.APIKey, error)
    getAPIKey          func(id int) (*database.APIKey, error)
    // ... 5 more
}

func (m *mockAPIKeyStore) CreateAPIKey(k *database.APIKey) (*database.APIKey, error) {
    return m.createAPIKey(k)
}
// ... etc.

func TestAPIKeyHandler_Create(t *testing.T) {
    store := &mockAPIKeyStore{
        createAPIKey: func(k *database.APIKey) (*database.APIKey, error) {
            k.ID = 42
            return k, nil
        },
    }
    h := NewAPIKeyHandler(store)

    w := httptest.NewRecorder()
    c, _ := gin.CreateTestContext(w)
    // set up request body, call h.Create(c), assert response
}
```

Key rule: **one mock struct per interface, defined in the test file that uses it** — not a shared mock registry.

---

## 7. Migration Order (Phased)

### Phase 1 — Proof of concept (2 domains, establishes pattern)
`auth` + `apikeys` → adds structs to existing `handlers/auth.go` and `handlers/apikeys.go`

Deliverables:
- `AuthHandler` + `APIKeyHandler` structs with narrow interfaces
- `wire_handlers.go` created (initially just routes for auth/apikeys, others still use `s.*`)
- `auth_handlers.go` and `apikey_handlers.go` deleted
- `AuthHandler_test.go`, `APIKeyHandler_test.go` with minimal mocks
- All existing tests pass

### Phase 2 — Small grouped domains (9 domains)
`reading`, `split_book`, `cache`, `organize`, `filesystem`, `user`, `plugins`, `activity`, `playlist`

All added as Handler structs to `handlers/` package. Route registration in `wire_handlers.go` grows. Source `*_handlers.go` files deleted.

### Phase 3 — Medium grouped domains (4 domains)
`ai`, `diagnostics`, `itunes`, `versions`, `operations_v2`

Same pattern. `ai_handlers.go` has 16 methods using `pipelineManager` and `aiScanStore` — these become narrow interfaces on `AIHandler`.

### Phase 4 — Large sub-packages (7 domains)
`entities`, `operations`, `system`, `dedup`, `duplicates`, `audiobooks`, `metadata`

Each gets `handlers/<domain>/handler.go` + `interfaces.go`. The two cache-heavy domains (`audiobooks`, `entities`) accept `*cache.Cache[T]` directly as struct fields (no wrapper).

**Order within Phase 4**: entities → operations → system → dedup → duplicates → audiobooks → metadata (save the two largest for last).

---

## 8. What Doesn't Change

- Route paths are **identical** — no API surface changes
- `internal/server/handlers/` type definitions (from ADR-003 Phase 1) stay as-is
- `internal/server/middleware/` is untouched
- The `Server` struct shrinks as handler fields (`audiobookService`, `metadataFetchService`, etc.) are no longer needed by `Server` directly — they're passed to handler constructors in `wireHandlers()`
- Permission middleware (`s.perm(...)`) stays on `Server` and is threaded through route registration as before

---

## 9. Verification Criteria

Per phase, the following must pass before merging:

- `make build-api` clean
- `make test` clean (all packages)
- `grep -r "func (s \*Server).*handler\|func (s \*Server).*Handler" internal/server/` — count should decrease by the number of methods migrated
- Zero new `s.Store()` calls added to `internal/server/server.go`
- New handler tests provide ≥1 test per public method on the new Handler struct
