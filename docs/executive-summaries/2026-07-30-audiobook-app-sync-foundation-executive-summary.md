<!-- file: docs/executive-summaries/2026-07-30-audiobook-app-sync-foundation-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 358c00c7-6ac7-4cc7-bea3-b5a0580c8224 -->
<!-- last-edited: 2026-07-30 -->

# Executive Summary: Listening to Your Library on Your Phone — The Groundwork

**Related spec:** `docs/specs/2026-07-29-abs-sync-api-design.md`
**Task package:** `docs/agent-tasks/abs-sync/`

## In plain language

The goal is to stop syncing your audiobooks through iTunes/Apple Books and instead
listen to them directly from your own server, using a normal iPhone app — downloading
books over WiFi, listening offline in the car, and having your place kept correctly no
matter which device you pick up next.

This round didn't finish that. It did something less visible and, it turned out, more
important: **it found out what the phone apps actually require before we built to the
wrong assumptions.**

## Why that mattered more than expected

The apps we want to use all speak the "Audiobookshelf" protocol — but that protocol has
**no maintained specification**. Its own documentation site says, in writing, that the
docs are out of date and no longer maintained.

So rather than trust the docs, we ran a real Audiobookshelf server ourselves, recorded
exactly what it says on every request, and then read the source code of two of the phone
apps line by line to see what they genuinely need.

That turned up problems that would each have looked like a mysterious bug months later:

- **Your listening positions could have been silently deleted.** One app *removes* any
  saved position that the server doesn't mention when the app checks in. If our server
  had sent that list in pages — the obvious thing to do for a 44,000-book library — the
  app would have quietly wiped your place in every book it didn't see, every time you
  opened the home screen. That is exactly the failure this whole project exists to avoid.

- **Our internal ID numbers were the wrong length.** One app chops book IDs at a fixed
  character position. Ours are shorter than it expects, so progress and bookmarks would
  have been saved against malformed addresses. We now issue a second, correctly-shaped ID
  purely for the apps.

- **Books you finished would have been stuck at 99% forever.** The same book reports
  three slightly different total lengths depending on where you measure — they disagree
  by about a twentieth of a second. Comparing exactly would mean "finished" never quite
  triggered.

- **All your listening time would have recorded as zero.** The apps send the same
  information under two similar names — one means "how much since last time," the other
  "how much in total." Reading the wrong one records nothing. A published reference
  implementation of this protocol has that exact bug today.

- **One app can't even log in** if a particular field comes back empty — and the
  reference implementation we could have copied returns exactly that empty value.

## What we also decided, and checked properly

- **The protocol choice was challenged rather than assumed.** We compared the
  alternatives (Jellyfin, Emby, Plex, Subsonic, OPDS). Audiobookshelf won on evidence:
  it has roughly eight actively-maintained iPhone apps versus one or two for the others,
  and it is the only one that can properly hand back listening progress accumulated
  while your phone was offline — which, for driving and commuting, is the whole point.

- **Nothing on your server becomes publicly reachable.** Every single endpoint stays
  behind your existing Cloudflare login. We verified in the apps' source code that they
  send the required credentials on every request, including the very first connection
  test — the step that trips up other apps. The one exception is cover images, which the
  iPhone home-screen widget fetches without credentials; those are book covers only,
  addressed by an unguessable ID, and nothing else is exposed.

- **You can listen entirely offline.** Downloading a book, playing it in airplane mode,
  and having your progress catch up when you reconnect is a first-class path, not an
  afterthought.

## What actually got built

Working, tested pieces that the rest depends on:

- Reading chapter marks out of your audiobook files, and stitching multi-file books into
  one continuous timeline — verified to match the real server's numbers exactly.
- Serving audio so that **seeking works** and interrupted downloads resume, tested
  against a real 115 MB book.
- Durable ID records so a book keeps its identity when it's renamed, moved, retagged, or
  merged with a duplicate.
- A tested rulebook for resolving conflicts when two devices disagree about your
  position — including the important case where a phone that was offline for hours must
  be able to move you *forward* but never *backward*.
- A test harness that compares our server's answers against the recordings from the real
  one, field by field, so a future change that would break an app fails a test here
  instead of failing in your car.

## Known gap, stated plainly

Our promise that a book keeps its identity currently holds for the main
duplicate-merging path, but **not yet for two others** — including one that deletes
records outright, where a lost link cannot be recovered afterwards. That work is
scheduled and scoped; until it lands, the durability guarantee is only partly true, and
we would rather say so than imply otherwise.

## What's next

The remaining work is the app-facing layer itself: logging in, browsing your library,
and playback. The groundwork above is what makes that layer safe to write — and there is
a documented, self-contained task for each remaining piece.
