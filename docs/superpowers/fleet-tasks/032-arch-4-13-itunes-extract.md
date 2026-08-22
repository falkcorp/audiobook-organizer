<!-- file: docs/superpowers/fleet-tasks/032-arch-4-13-itunes-extract.md -->
<!-- version: 1.0.0 -->
<!-- guid: e2d08a63-3862-4985-89e2-562020ed5a4e -->
<!-- last-edited: 2026-08-22 -->

# Task 032: 4.13 — Extract iTunes integration into `internal/itunes` service

**Depends on:** none
**Estimated effort:** L
**Wave:** 9 (architecture)

## Goal

Decouple `itunesservice.Service` from server-bound closures (`OnBookCreated`, `OrganizerFactory`).
The iTunes service currently depends on `*Server` through closures; replace these with proper
interface injection and/or event bus subscription.

## Context

- Current coupling: `OnBookCreated` closure (server notifies iTunes when a book is created),
  `OrganizerFactory` closure (iTunes needs to create an organizer)
- Proposed fix from TODO.md:
  - Introduce a `BookCreated` event on the existing `eventbus` (already registered in the container)
  - Dedup engine subscribes with its own bg-context management
  - iTunes service publishes when `CreateBook` succeeds (removes `OnBookCreated` closure)
  - `OrganizerFactory` can move into itunesservice itself — `organizer.NewOrganizer(&config.AppConfig)`
    doesn't actually need `*Server`
- The iTunes service already lives in `internal/itunes/service/`
- Container: `internal/serviceregistry/` or `internal/container/` — check where services are wired

## Files to modify

- `internal/itunes/service/service.go` (or main iTunes service file) — remove closure fields,
  add event bus subscription
- `internal/server/` (iTunes registration) — update to pass event bus instead of closures
- `internal/eventbus/` (or wherever the event bus lives) — add `BookCreated` event type if missing
- `internal/container/` or `internal/serviceregistry/` — update wiring

## Instructions

### 1. Audit current coupling

```bash
grep -n "OnBookCreated\|OrganizerFactory\|*Server" internal/itunes/service/service.go
```

List every `*Server` dependency. Most should be eliminable via the approaches below.

### 2. Define `BookCreated` event

In the event bus package (find it: `grep -rn "eventbus\|EventBus\|Publish\|Subscribe" internal/ --include="*.go" | head -20`):
```go
type BookCreatedEvent struct {
    BookID string
    Book   *database.Book
}
```

### 3. Replace `OnBookCreated` closure with event subscription

In iTunes service:
```go
func (s *Service) Start(ctx context.Context) error {
    s.eventBus.Subscribe("book.created", func(e eventbus.Event) {
        evt := e.(BookCreatedEvent)
        go s.handleBookCreated(ctx, evt.Book)
    })
    return nil
}
```

In server/audiobook handlers, after `CreateBook` succeeds:
```go
s.eventBus.Publish("book.created", BookCreatedEvent{BookID: book.ID, Book: book})
```

### 4. Replace `OrganizerFactory` closure

In `itunes/service/service.go`, instead of a factory closure, call:
```go
org := organizer.NewOrganizer(&s.config)
```

This removes the need for `*Server` entirely for this purpose. Check that `organizer.NewOrganizer`
only needs `*config.AppConfig` — if it needs other deps, extract those as interface fields on the
iTunes service struct.

### 5. Update container wiring

Remove closure-based wiring in `NewServer` or the container. Replace with event bus + config injection.

### 6. Update ServerDeps

If `ServerDeps` struct passes fields to the iTunes service, convert each field to an explicit
interface instead of the full struct.

## Test

```bash
go build ./...
go test ./internal/itunes/... -v -count=1
go test ./internal/server/... -v -count=1
make ci
```

## Commit

```
refactor(itunes): decouple iTunes service from *Server closures via event bus (4.13)
```

## PR title

`refactor(itunes): decouple from server closures — 4.13`

## After merging

Mark `- [~] **PLUGIN-DECOUPLE-SERVER-CLOSURES**` as `- [x]` in `TODO.md`.
Mark `- [ ] **4.13**` as `- [x]`.
