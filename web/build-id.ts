// file: web/build-id.ts
// version: 1.0.0
// guid: 055a6ad9-dfe3-41b6-9aab-b74694012213
// last-edited: 2026-11-08

// The UI build ID rule (07 §7.6.3, 03 §3.6 "Build identity") and the Vite
// plugin that writes it into index.html. It lives beside vite.config.ts, not
// in it, so a unit test can reach every branch without loading the React and
// babel plugins the config imports.
//
// The ID names one built UI bundle. It is written ONLY into
// <meta name="mediacore-build" content="...">: no `define`, no env value
// (FE6-1). The server's X-App-Build and `--version-json`'s ui_build_id read
// it back from the embedded index.html, and the page reads it once at load.

/** What an ID may contain: it lands in an HTML attribute and an HTTP header. */
export const BUILD_ID_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;

/** The commit a clean checkout is at, or null when the tree is dirty or has no git data. */
export interface CleanCommit {
  shortSha: string;
  /** Commit time, unix seconds. */
  unixTime: number;
}

export interface BuildIdSources {
  /** The BUILD_ID environment variable; empty or unset falls through. */
  env?: string;
  /** Null when `git status --porcelain --untracked-files=no` is not empty, or git/.git is absent. */
  cleanCommit: () => CleanCommit | null;
  now: () => Date;
  /** n random lowercase hex characters. */
  randomHex: (n: number) => string;
}

/** The UTC timestamp part of a dirty-tree ID: sortable, and only [0-9TZ]. */
function utcStamp(d: Date): string {
  return d.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
}

/**
 * resolveBuildId is the one rule:
 *
 *  1. BUILD_ID when non-empty. It must match BUILD_ID_PATTERN and must not be
 *     `dev` in any case (reserved: a `dev` ID silently disables the reload
 *     rule), else the build fails. An empty value falls through.
 *  2. In a clean checkout, `<short sha>-<commit unix time>`, so a rebuild of
 *     one clean commit gets one ID.
 *  3. Otherwise (dirty tree, or no .git as in the Docker context),
 *     `<UTC timestamp>-<8 random hex>`, so two dirty builds of one commit
 *     never share an ID.
 */
export function resolveBuildId(src: BuildIdSources): string {
  const fromEnv = src.env ?? '';
  if (fromEnv !== '') {
    if (!BUILD_ID_PATTERN.test(fromEnv)) {
      throw new Error(
        `BUILD_ID ${JSON.stringify(fromEnv)} is not a valid UI build ID: it must match ${BUILD_ID_PATTERN}`
      );
    }
    if (fromEnv.toLowerCase() === 'dev') {
      throw new Error(
        'BUILD_ID "dev" is reserved: a dev build ID disables the stale-build reload rule, so a production bundle must not carry it'
      );
    }
    return fromEnv;
  }
  const clean = src.cleanCommit();
  if (clean) {
    return `${clean.shortSha}-${clean.unixTime}`;
  }
  return `${utcStamp(src.now())}-${src.randomHex(8)}`;
}

const META_TAG = /<meta\b[^>]*\bname="mediacore-build"[^>]*>/i;

/**
 * stampBuildId returns html with the mediacore-build meta tag's content set to
 * id. The tag must exist and carry a content attribute (index.html's
 * placeholder), else a build would ship a bundle with no ID and the server
 * would answer `dev` for it; so a missing tag is an error, not a no-op.
 * id has passed BUILD_ID_PATTERN or is generated, so it is attribute-safe.
 */
export function stampBuildId(html: string, id: string): string {
  const tag = META_TAG.exec(html);
  if (!tag || !/\bcontent="[^"]*"/i.test(tag[0])) {
    throw new Error('index.html has no <meta name="mediacore-build" content="..."> to stamp the UI build ID into');
  }
  const stamped = tag[0].replace(/\bcontent="[^"]*"/i, `content="${id}"`);
  return html.replace(tag[0], () => stamped);
}

/** The minimal Vite plugin shape this file needs (so the tests need no vite types). */
export interface BuildIdPlugin {
  name: string;
  transformIndexHtml: { order: 'pre'; handler: (html: string) => string };
}

/**
 * mediacoreBuildPlugin stamps id into index.html. The config calls
 * resolveBuildId once, when the command is `build`, so a bad BUILD_ID fails
 * the build before any bundling; `vite serve` passes null, leaves the
 * placeholder's `dev` in place and never runs git.
 */
export function mediacoreBuildPlugin(id: string | null): BuildIdPlugin {
  return {
    name: 'mediacore-build-id',
    transformIndexHtml: {
      order: 'pre',
      handler: (html: string): string => (id === null ? html : stampBuildId(html, id)),
    },
  };
}
