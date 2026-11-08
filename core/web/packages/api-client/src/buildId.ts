// file: core/web/packages/api-client/src/buildId.ts
// version: 1.0.0
// guid: 6542cffb-01e6-48e7-a6e6-15293ec29bee
// last-edited: 2026-11-08

// The stale-build rule (07 §7.5.2; 03 §3.6 "Build identity"): one rule in one
// place. The server stamps every response with X-App-Build, the UI build ID of
// the bundle it embeds, which `vite build` wrote into the page's
// <meta name="mediacore-build">. A tab left open across an upgrade, or across a
// rollback to a server that predates the header, holds a bundle whose lazy chunks
// may be gone, so it is offered a reload.
//
// Both inputs are runtime values the CALLER passes, never defines read here: this
// package is built to dist/ before and separately from the app, so a define baked
// into it would carry the library build's own ID (or undefined) and every
// production tab would be offered a permanent reload (FE6-1, FE6-8). Both options
// are required, so an omitted buildId is a compile error, never a silently
// disabled check (FE7-5).

/**
 * Why a response says this tab's bundle is stale (the three cases of 07 §7.5.2):
 *  - `header-mismatch` (case 1): X-App-Build names a different bundle than this page's.
 *  - `legacy-spa` (case 2, `checkModules`, the shell only): the server's modules
 *    document says it serves no shell bundle, so it is a pre-shell release behind
 *    a shell tab.
 *  - `pre-r1-server` (case 3): the response carries `Reporting-Endpoints: csp=...`,
 *    so it is the app's, but no X-App-Build, so the server predates the header (an
 *    R1+ tab after a rollback to R0).
 */
export type StaleReason = 'header-mismatch' | 'legacy-spa' | 'pre-r1-server';

/** The two inputs of every check. Both required. */
export interface BuildCheckOptions {
  /** The page's build ID, from `readPageBuildId()`. `""` and `"dev"` never trigger the rule. */
  buildId: string;
  /** True for the shell's calls, false for the pre-shell bundle's. Never read from `import.meta.env`. */
  shell: boolean;
}

/** The part of `/api/core/v1/modules`'s body that case 2 reads. */
export interface ModulesResponse {
  app?: { ui_build_id?: string };
}

const BUILD_META_SELECTOR = 'meta[name="mediacore-build"]';

/**
 * The only reader of `<meta name="mediacore-build">`: its content, `""` when the
 * tag is absent (or there is no document). Callers read it once, at page load.
 */
export function readPageBuildId(): string {
  if (typeof document === 'undefined') return '';
  return document.querySelector(BUILD_META_SELECTOR)?.getAttribute('content')?.trim() ?? '';
}

/** An ID that carries no identity: an absent tag or header, or `vite serve`'s `dev`. */
function isUnknownBuildId(id: string): boolean {
  return id === '' || id.toLowerCase() === 'dev';
}

/** The response headers, or null for a test double that has none (never stale). */
function headersOf(response: Response): Headers | null {
  return typeof response.headers?.get === 'function' ? response.headers : null;
}

const detectedReasons: StaleReason[] = [];
const staleListeners = new Set<(reason: StaleReason) => void>();

function noteStale(reason: StaleReason): void {
  if (detectedReasons.includes(reason)) return;
  detectedReasons.push(reason);
  for (const listener of [...staleListeners]) listener(reason);
}

/**
 * Applies cases 1 and 3 to one response, headers only: it is synchronous and never
 * consumes a body, and it does not special-case `/modules` (case 2 is
 * `checkModules`). Returns the reason the response shows the page stale, or null,
 * and notifies the `onStaleBuild` listeners of a reason once per page load.
 *
 * A `buildId` of `""` or `"dev"` never triggers it, and neither does a header of
 * `""` or `"dev"` (stage 5's "unknown", never a mismatch). `shell` is not read here
 * (cases 1 and 3 hold for both bundles); it is required so that every caller states
 * which bundle it is, in the one signature (FE6-8).
 */
export function checkBuild(response: Response, opts: BuildCheckOptions): StaleReason | null {
  if (isUnknownBuildId(opts.buildId)) return null;
  const headers = headersOf(response);
  if (!headers) return null;

  const header = (headers.get('X-App-Build') ?? '').trim();
  let reason: StaleReason | null = null;
  if (header !== '') {
    if (!isUnknownBuildId(header) && header !== opts.buildId) reason = 'header-mismatch';
  } else if (
    (response.status ?? 200) < 500 &&
    /(^|[\s,])csp\s*=/i.test(headers.get('Reporting-Endpoints') ?? '')
  ) {
    // The app's own answer (HEAD and every build since write the header) with no
    // X-App-Build. A proxy's 502 or an Access interstitial has neither header and
    // never counts.
    reason = 'pre-r1-server';
  }
  if (reason) noteStale(reason);
  return reason;
}

/**
 * Applies case 2 to the parsed `/api/core/v1/modules` body, which the shell's own
 * fetch has read anyway, so it never consumes the body its caller reads. The
 * caller must be the shell (`shell: true`; a pre-shell bundle seeing an empty
 * `ui_build_id` IS the legacy SPA, and a reload would load the same bundle), the
 * body's `app.ui_build_id` must be `""` and X-App-Build must be neither empty nor
 * `dev`: the server serves a legacy SPA at `/`.
 */
export function checkModules(
  body: ModulesResponse,
  response: Response,
  opts: BuildCheckOptions,
): StaleReason | null {
  if (!opts.shell || isUnknownBuildId(opts.buildId)) return null;
  const headers = headersOf(response);
  if (!headers) return null;
  const header = (headers.get('X-App-Build') ?? '').trim();
  if (isUnknownBuildId(header)) return null;
  if ((body?.app?.ui_build_id ?? '') !== '') return null;
  noteStale('legacy-spa');
  return 'legacy-spa';
}

/** The reasons detected on this page load, each once. */
export function getStaleReasons(): readonly StaleReason[] {
  return detectedReasons;
}

/**
 * Subscribes to stale-build detections, whichever transport saw the response (a
 * native call through the client or a legacy `apiFetch`). The listener is called
 * at once for each reason already detected, so a banner that mounts after the
 * page's first stale response (the auth probe) still hears it, and then once per
 * new reason. Returns the unsubscribe function.
 */
export function onStaleBuild(listener: (reason: StaleReason) => void): () => void {
  staleListeners.add(listener);
  for (const reason of [...detectedReasons]) listener(reason);
  return () => {
    staleListeners.delete(listener);
  };
}
