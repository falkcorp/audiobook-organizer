### Fixed

#### Validate the two unvalidated client-side navigation sinks

`web/src/pages/Login.tsx`'s post-login `redirectTo` (from `location.state.from`)
and `web/src/pages/BookDetail.tsx`'s two "back to library" navigations (from
`sessionStorage['library_return_url']`) previously handed an unvalidated
string straight to `navigate()`. Neither was exploitable today only because
nothing in the codebase currently writes an attacker-reachable value into
either sink, but that made them latent open-redirect targets for the next
feature that writes to `location.state.from` or `library_return_url`. Both
now run through a new `sanitizeReturn` in `web/src/utils/safeReturn.ts` — a
direct TypeScript port of the same-named guard already protecting the
server-side redirect target in `internal/server/handlers/oauth_login.go`
(single leading `/`, no second leading `/` or `\`, no backslash anywhere) —
and fall back to `/dashboard` and `/library` respectively when the value is
rejected, rather than navigating to it or throwing.
