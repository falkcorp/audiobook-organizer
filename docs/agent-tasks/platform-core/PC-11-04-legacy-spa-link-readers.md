<!-- file: docs/agent-tasks/platform-core/PC-11-04-legacy-spa-link-readers.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6143a038-214c-4460-bf8d-d72d822c6af1 -->
<!-- last-edited: 2026-11-04 -->

# PC-11-04 — Legacy SPA link readers ahead of the flip: the /login/temp route and the /invite#token= reader

**ID:** PC-11-04 · **Wave:** W11 (S4 r2) · **Priority:** P1 · **Effort:** S
**Tier:** T1 Sonnet/medium — two client-side readers that nothing links to until the flip's 302s (reader before writer,
09 §9.2.1); every edit is listed below.
**Depends on:** PC-11-01. Lands before the flip PC-11-02 and ships in S4 r2 with it.
**Stage-Release trailer:** `Stage-Release: S4 2` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Behaviour rules 10, 11).
**Owning spec:** 09 S4 "Invite and temp-login links"; 07 §7.4.3 (fragment tokens; the invite reader), §7.9.1 (the
`/login/temp` route in both branches); 05-authn §4.13 (`POST /api/core/v1/auth/temp-login/consume` `{token}`, sets the
cookie); 09 §9.2.1 "Expand" (reader before writer).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-11-04" -b agent/pc-11-04-spa-link-readers origin/main
cd "$REPO/../audiobook-organizer-pc-11-04"
git rebase origin/main
git worktree list
npm ci --prefix web
```

## Exact files

Create: `web/src/routes/LoginTemp.tsx`, `web/src/routes/LoginTemp.test.tsx`, `web/src/routes/AcceptInvite.fragment.test.tsx`.
Modify: `web/src/App.tsx`; `web/src/pages/AcceptInvite.tsx` (see Planner notes: not in the row's list, required by the
reader).

## Re-verify (expected at the plan checkout `f07f516be`; UI-0's react-router 8 upgrade, PC-08-01, may change import
sources and line numbers — re-take them, keep the rule)

```bash
grep -n "if (location.pathname.startsWith('/invite/')) {" web/src/App.tsx                 # 1 hit (:201)
grep -n '<Route path="/invite/:token" element={<AcceptInvite />} />' web/src/App.tsx      # 1 hit (:204)
grep -n '<Route path="/login" element={<Login />} />' web/src/App.tsx                     # 1 hit (:249, unauthenticated branch)
grep -n '<Route path="/login" element={<Navigate to="/dashboard" replace />} />' web/src/App.tsx   # 1 hit (:313, authenticated branch)
grep -n "from 'react-router-dom'\|from 'react-router'" web/src/App.tsx                    # 1 hit: note the package; use the same one everywhere below
grep -n "const { token = '' } = useParams<{ token: string }>();" web/src/pages/AcceptInvite.tsx   # 1 hit (:151)
grep -n "import { apiFetch } from '../utils/apiFetch';" web/src/pages/AcceptInvite.tsx     # 1 hit
ls web/src/routes 2>/dev/null || echo absent                                               # absent (created here)
grep -rn 'temp-login/consume' web/src | wc -l                                              # 0
```

## Steps

1. **`web/src/routes/LoginTemp.tsx`** (4-line header, new guid). Export two helpers and the page:
   ```ts
   export function readFragmentToken(): string | null   // parses window.location.hash: '#token=<t>' -> '<t>' (URLSearchParams over hash.slice(1)); else null. Pure: no side effect.
   export function stripFragment(): void                // window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search)
   export function LoginTemp(): JSX.Element
   ```
   `LoginTemp`: `const [token] = useState(readFragmentToken);` then one `useEffect` that calls `stripFragment()` and, if
   `token` is set and a `useRef` guard is false (set it true first, so React StrictMode's second effect run never sends a
   second request), calls `apiFetch('/api/core/v1/auth/temp-login/consume', { method: 'POST', headers:
   { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) })`. On `resp.ok`: `window.location.replace('/')`
   (a full load picks up the session cookie). On failure or a missing token: render an MUI `Alert` with
   `"This sign-in link is invalid or has expired."` and a link to `/login`. While waiting: a `CircularProgress`.
2. **`web/src/App.tsx`**:
   - add `import { LoginTemp } from './routes/LoginTemp';` after the `AcceptInvite` import;
   - change `if (location.pathname.startsWith('/invite/')) {` to
     `if (location.pathname === '/invite' || location.pathname.startsWith('/invite/')) {`;
   - inside that block's `<Routes>`, add `<Route path="/invite" element={<AcceptInvite />} />` directly above
     `<Route path="/invite/:token" element={<AcceptInvite />} />`;
   - in the unauthenticated branch, add `<Route path="/login/temp" element={<LoginTemp />} />` directly above
     `<Route path="/login" element={<Login />} />`;
   - in the authenticated branch, add `<Route path="/login/temp" element={<LoginTemp />} />` directly above
     `<Route path="/login" element={<Navigate to="/dashboard" replace />} />`;
   - bump the header version (minor) and `last-edited`.
3. **`web/src/pages/AcceptInvite.tsx`**: import `readFragmentToken` and `stripFragment` from `'../routes/LoginTemp'`;
   replace `const { token = '' } = useParams<{ token: string }>();` with
   ```ts
   const { token: pathToken = '' } = useParams<{ token: string }>();
   const [fragmentToken] = useState(readFragmentToken);
   const token = fragmentToken ?? pathToken;
   useEffect(() => { if (fragmentToken) stripFragment(); }, [fragmentToken]);
   ```
   (place the `useEffect` after the existing `useState` lines; `useState` and `useEffect` are already imported). Change
   nothing else: the page keeps calling the legacy invite routes; PC-11-02 switches them to the core routes. Bump the
   header.
4. **`web/src/routes/LoginTemp.test.tsx`** (vitest + Testing Library; stub `fetch` the same way
   `web/src/pages/AcceptInvite.test.tsx` does; set the URL with `window.history.replaceState(null, '',
   '/login/temp#token=abc')` and render with `renderWithProviders(<LoginTemp />, { initialEntries: ['/login/temp'] })`):
   - `it('strips the fragment and calls the consume route')` (the row's new test): the request goes to
     `/api/core/v1/auth/temp-login/consume` with method `POST` and body `{"token":"abc"}`, and afterwards
     `window.location.hash === ''`; exactly one request is sent;
   - `it('shows an error and sends nothing without a token')`.
5. **`web/src/routes/AcceptInvite.fragment.test.tsx`**: `describe('TestLegacySPA_InviteFragmentReader', ...)` (the name
   09 S4, 05-authn §10 and 07 use): with the URL `/invite#token=t1` and routes `/invite` and `/invite/:token` both
   rendering `AcceptInvite`, the page requests `/api/v1/auth/invite/t1` and the hash is empty afterwards; a second case
   with `/invite/t2` (path form) still requests `/api/v1/auth/invite/t2`.
6. Add a `changelog.d/` fragment (no header).

## Acceptance (expected output)

```bash
cd web
npx vitest run src/routes/LoginTemp.test.tsx src/routes/AcceptInvite.fragment.test.tsx src/pages/AcceptInvite.test.tsx   # all tests passed
npx eslint src/routes src/App.tsx src/pages/AcceptInvite.tsx                                                          # no errors
npx tsc --noEmit -p .                                                                                                 # exit 0
cd ..
grep -c '<Route path="/login/temp" element={<LoginTemp />} />' web/src/App.tsx    # 2
grep -c '<Route path="/invite" element={<AcceptInvite />} />' web/src/App.tsx     # 1
grep -n "location.pathname === '/invite' || location.pathname.startsWith('/invite/')" web/src/App.tsx   # 1 hit
make ci                                                                           # exit 0
```

## Out of scope

The temp-login 302 and core's link minting (PC-11-02); switching `AcceptInvite` to the core invite routes (PC-11-02); the
end-to-end tests `TestLegacyTempLoginLink_EndToEnd` and `TestLegacyInviteLink_EndToEnd` (PC-11-02); the shell's
`/invite` page (S9).

## Rollback

Revert: nothing links to `/login/temp` or `/invite#token=` before the flip.

## Repo rules

Headers on every changed and new file. Conventional commit, e.g. `feat(web): read temp-login and invite tokens from the
URL fragment`, with `Stage-Release: S4 2` and ending with `Co-Authored-By: Claude <noreply@anthropic.com>`.
`gh pr create`, then `gh pr merge <n> --rebase` after the gate.

## Planner notes

- **Missing file:** `web/src/pages/AcceptInvite.tsx` must change for the fragment reader (it reads the token only from
  the path, `useParams`, :151 at the plan checkout); the row lists only `web/src/App.tsx`. This brief edits it; PC-11-02
  edits it again for the core invite calls.
- **Path convention:** the repo keeps pages in `web/src/pages/`; there is no `web/src/routes/` directory at the plan
  checkout. The brief keeps the row's paths; the coordinator may move `LoginTemp` to `web/src/pages/` before execution.
- `TestLegacySPA_InviteFragmentReader` is an e2e done-check in 07 §12; this PR names its unit half with the same title and
  PC-11-02 carries the end-to-end cell.
