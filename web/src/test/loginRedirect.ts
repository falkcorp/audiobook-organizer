// file: web/src/test/loginRedirect.ts
// version: 1.1.0
// guid: 9dd4414b-27c3-4c41-b366-5bf09fa26b59
// last-edited: 2026-10-10

/**
 * What an expired Cloudflare Access session looks like to the browser: a 200
 * response carrying an HTML login page for a URL under /api/. apiFetch turns
 * this into ApiAuthRedirectError; the page tests stub fetch with it and assert
 * the page shows an error rather than treating the response as success.
 */
export function loginPageResponse(): Response {
  return new Response('<html><body>login</body></html>', {
    status: 200,
    headers: { 'Content-Type': 'text/html' },
  });
}

/**
 * A login bounce whose body would parse as a perfectly good API answer: JSON
 * text served with a text/html content-type. Code that goes through apiFetch
 * rejects it; code that calls raw fetch and parses the body "succeeds". Tests
 * for best-effort loaders use it so they fail if the apiFetch call is reverted
 * (a plain HTML body would be swallowed by the loader's own JSON parse error
 * and pass either way).
 */
export function loginPageWithJsonBody(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'text/html' },
  });
}
