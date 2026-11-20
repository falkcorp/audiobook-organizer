// file: core/web/packages/shell/src/version/diagnostics.ts
// version: 0.1.0
// guid: c06b2690-5e0c-4525-bef4-6a2fc829826a
// last-edited: 2026-11-20

// Reporting of intersection diagnostics (07 §7.5.2): once per kind per page
// load to POST /api/core/v1/ui/diagnostics (body <= 4 KiB).

import type { Diagnostic } from './types';

export const DIAGNOSTICS_PATH = '/api/core/v1/ui/diagnostics';
export const MAX_DIAGNOSTIC_BODY_BYTES = 4096;
/** The server bounds kind and module at 64 bytes each (internal/uihttp labelMax). */
const LABEL_MAX_BYTES = 64;

const reportedKinds = new Set<string>();
const encoder = new TextEncoder();

/** Cuts `s` to at most `max` UTF-8 bytes without splitting a character. */
export function truncateBytes(s: string, max: number): string {
  if (encoder.encode(s).length <= max) return s;
  let out = '';
  let used = 0;
  for (const ch of s) {
    const n = encoder.encode(ch).length;
    if (used + n > max) break;
    out += ch;
    used += n;
  }
  return out;
}

/** The request body for one diagnostic. The server reads only kind and module, so only those are sent. */
export function diagnosticBody(d: Diagnostic): string {
  const body: { kind: string; module?: string } = { kind: truncateBytes(d.kind, LABEL_MAX_BYTES) };
  if (d.module) body.module = truncateBytes(d.module, LABEL_MAX_BYTES);
  let json = JSON.stringify(body);
  if (encoder.encode(json).length > MAX_DIAGNOSTIC_BODY_BYTES) {
    json = JSON.stringify({ kind: truncateBytes(d.kind, LABEL_MAX_BYTES) });
  }
  return json;
}

/**
 * Reports each diagnostic kind once per page load. Debug-level entries are
 * never reported. A failed POST releases its kind so the next intersection
 * retries it; the failure is logged, not thrown.
 */
export async function reportDiagnostics(fetchFn: typeof fetch, diagnostics: readonly Diagnostic[]): Promise<void> {
  const fresh: Diagnostic[] = [];
  for (const d of diagnostics) {
    if (d.level === 'debug' || reportedKinds.has(d.kind)) continue;
    reportedKinds.add(d.kind);
    fresh.push(d);
  }
  await Promise.all(
    fresh.map(async (d) => {
      try {
        const res = await fetchFn(DIAGNOSTICS_PATH, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: diagnosticBody(d),
        });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
      } catch (err) {
        reportedKinds.delete(d.kind);
        console.warn(`could not report UI diagnostic ${d.kind}`, err);
      }
    }),
  );
}
