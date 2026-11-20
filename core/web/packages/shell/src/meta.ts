// file: core/web/packages/shell/src/meta.ts
// version: 0.1.0
// guid: 7f1d3a68-9c45-4e02-a6b8-1e0c7d3f5a92
// last-edited: 2026-11-20

// Readers of the preset's metas (07 §7.7, §7.10). The build-ID meta has one
// reader, the api-client's readPageBuildId.

export interface ThemeMeta {
  storageKey: string;
  defaultMode: 'light' | 'dark';
}

function metaContent(name: string): string | null {
  if (typeof document === 'undefined') return null;
  const c = document.querySelector(`meta[name="${name}"]`)?.getAttribute('content');
  return c == null ? null : c.trim();
}

/** <meta name="mediacore-app">, or null when the page has none. */
export function readAppMeta(): string | null {
  const c = metaContent('mediacore-app');
  return c === '' ? null : c;
}

/** <meta name="mediacore-theme" content="<key> <mode>">, or null when absent or malformed. */
export function readThemeMeta(): ThemeMeta | null {
  const c = metaContent('mediacore-theme');
  if (!c) return null;
  const m = /^([A-Za-z0-9._:-]{1,128}) (light|dark)$/.exec(c);
  return m ? { storageKey: m[1], defaultMode: m[2] as 'light' | 'dark' } : null;
}
