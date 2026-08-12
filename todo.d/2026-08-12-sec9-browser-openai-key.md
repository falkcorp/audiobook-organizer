## Security

- [ ] **SEC-9: the OpenAI API key is sent from the browser.**
      `web/src/components/wizard/WelcomeWizard.tsx:147-160` calls
      `fetch('https://api.openai.com/v1/models', { Authorization: \`Bearer ${openaiKey}\` })`
      directly from the client during setup, to validate the key the user just typed.

      This puts the key in the browser's network log, in any extension with request access, and
      in whatever the user's corporate TLS-inspecting proxy keeps. It was flagged as SEC-9 in
      `docs/audits/2026-06-22-repo-optimization-security-sweep.md` and is still live seven weeks
      later — surfaced again 2026-08-12 while assessing that audit for archivability.

      The fix is a server-side validation endpoint: POST the key to the backend, let the backend
      call OpenAI, return valid/invalid. The key then never leaves the origin. The wizard flow
      does not change from the user's point of view.

      Sibling findings from the same audit that ARE fixed (so this is not a stale doc):
      SEC-1 (committed `abk_` key), SEC-3 (temp-login trusting the `Host` header,
      `auth_temp_login.go:128`), SEC-4 (security headers, `server_middleware.go:103-109`),
      TOOL-2 (`mockery ... || true` removed from CI), TOOL-8 (2026-08-10).
