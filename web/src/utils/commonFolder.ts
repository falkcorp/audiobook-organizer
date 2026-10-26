// file: web/src/utils/commonFolder.ts
// version: 1.0.0
// guid: e5a29c47-8d13-4f60-b7c2-1a9f3d6e0b84
// last-edited: 2026-10-26

/**
 * The folder that holds every one of `paths`: the longest shared run of leading
 * directory segments. One file gives its own folder. Returns '' when the paths
 * share no folder (or there are none). Both '/' and '\' separate segments; the
 * first path's separator style is kept in the result.
 */
export function commonFolder(paths: string[]): string {
  const clean = paths.filter((p) => p.length > 0);
  if (clean.length === 0) return '';
  const sep = clean[0].includes('\\') && !clean[0].includes('/') ? '\\' : '/';
  const dirs = clean.map((p) => {
    const parts = p.split(/[\\/]/);
    parts.pop(); // the file name
    return parts;
  });
  let shared = dirs[0];
  for (const d of dirs.slice(1)) {
    let i = 0;
    while (i < shared.length && i < d.length && shared[i] === d[i]) i++;
    shared = shared.slice(0, i);
  }
  const joined = shared.join(sep);
  // A file directly under the root keeps the root: ["", "x.m4b"] -> "" -> "/".
  return joined === '' && clean[0].startsWith(sep) ? sep : joined;
}
