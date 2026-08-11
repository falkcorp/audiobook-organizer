- [ ] 🚨 **E2E runs in one worktree can silently be served by a DIFFERENT
      worktree's server, and `global-setup.ts` does not catch it.** Hit for real
      on 2026-08-11 while gating `fix/library-load-freeze`. This is a false-green
      generator and it affects every agent running e2e concurrently.

      **What happened.** `playwright.config.ts` hardcodes `127.0.0.1:8484` and
      sets `reuseExistingServer: !process.env.CI`. There were 11 worktrees
      checked out. The sequence:

      1. Killed `:8484`, confirmed free.
      2. Ran `npm run build && go build` in my worktree (~3 minutes).
      3. During that window, a sibling worktree's agent started ITS server on
         `:8484`.
      4. My server launched, failed with
         `listen tcp 127.0.0.1:8484: bind: address already in use`, and did not
         exit — it just never served.
      5. `curl :8484` returned `200`, so everything looked healthy.
      6. My gate ran green-path assertions against the **sibling's build**, which
         contained none of my changes, and reported 4 failed / 1 passed.

      I only noticed because the numbers were *identical* to the pre-fix run —
      42,017 DOM nodes, requested limits `["1000","20"]`. Had my change been a
      no-op in the other direction, this would have been a **false green** and I
      would have shipped it.

      **Why the existing guard misses it.** `global-setup.ts` asserts the served
      bundle is not older than local build artifacts. A sibling that just rebuilt
      passes that check comfortably. The guard answers *"is it stale?"* — the
      question that matters here is *"is it mine?"*

      **Fix shape**, either or both:

      1. **Derive the port from the worktree** so collisions are impossible —
         e.g. hash the worktree path into a port, or read `E2E_PORT` with a
         per-worktree default. Then two agents cannot contend at all. (Workaround
         used on the day: a throwaway config with `--port 8585` and
         `reuseExistingServer: false`. It worked — full chromium suite 283
         passed / 0 failed / 21 skipped — but it was scratch, not committed.)
      2. **Assert identity, not freshness.** Have `global-setup.ts` fetch the
         served `index.html`, resolve the hashed asset filenames, and require
         they match the filenames present in the local `web/dist`. A sibling's
         bundle has different content hashes, so this fails immediately and
         loudly.

      Also worth fixing: a server that fails to bind should **exit non-zero**
      rather than staying alive doing nothing. That hung process is what made
      `lsof`/`ps` look reassuring while the port belonged to someone else.
