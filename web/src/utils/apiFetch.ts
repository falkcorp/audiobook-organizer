// file: web/src/utils/apiFetch.ts
// version: 1.0.0
// guid: c1d2e3f4-a5b6-7890-cdef-012345678901
// last-edited: 2026-06-22

/**
 * apiFetch is a thin wrapper around the browser Fetch API that standardises
 * the options every service call needs:
 *
 *   - credentials: 'include'  — sends session cookies on every request
 *   - Content-Type: application/json — added automatically for non-GET
 *     requests that have a body (unless the caller sets it explicitly)
 *
 * AbortSignal is forwarded transparently via options.signal.
 *
 * Usage:
 *   const response = await apiFetch('/api/v1/audiobooks');
 *   const response = await apiFetch('/api/v1/audiobooks/123', {
 *     method: 'PUT',
 *     body: JSON.stringify(payload),   // Content-Type set automatically
 *     signal: controller.signal,
 *   });
 */
export async function apiFetch(url: string, options: RequestInit = {}): Promise<Response> {
  const method = (options.method ?? 'GET').toUpperCase();
  const headers = new Headers(options.headers);

  if (
    options.body !== undefined &&
    method !== 'GET' &&
    method !== 'HEAD' &&
    !headers.has('Content-Type')
  ) {
    headers.set('Content-Type', 'application/json');
  }

  return fetch(url, {
    credentials: 'include',
    ...options,
    headers,
  });
}
