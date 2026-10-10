// file: web/src/test/loginRedirect.ts
// version: 1.0.0
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
