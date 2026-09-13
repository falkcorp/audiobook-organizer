// file: web/src/utils/coverUrl.ts
// version: 1.0.0
// guid: 4b1e7c2a-9d3f-4e58-a6b0-2c7f1d8e5a93
// last-edited: 2026-09-13

// Amazon/Audible image URLs carry a size directive between the image id and the
// extension -- `…/I/51abc._SL500_.jpg`, `…/I/51abc._SX342_.jpg`,
// `…/I/51abc._SL500_AC_.jpg`. Dropping the directive (`…/I/51abc.jpg`) makes
// the image host return the original upload rather than a resized copy.
const AMAZON_IMAGE_HOST = /^https?:\/\/([a-z0-9-]+\.)*(media-amazon\.com|ssl-images-amazon\.com|images-amazon\.com)\//i;
const AMAZON_SIZE_TOKEN = /\._[A-Z0-9_,]+_(\.[a-z]+)$/i;

/**
 * Returns the URL to use when showing a cover ENLARGED. For provider URLs whose
 * size is encoded in the URL, this is the largest variant; for anything else the
 * input is returned unchanged.
 *
 * Only rewrites shapes that are known to have a larger variant at the rewritten
 * URL. Google Books' `zoom=` parameter is deliberately left alone: a higher zoom
 * value is not reliably served for every volume, and a rewrite that 404s would
 * trade a small cover for none.
 */
export function coverFullSizeUrl(url: string): string {
  if (!AMAZON_IMAGE_HOST.test(url)) return url;
  return url.replace(AMAZON_SIZE_TOKEN, '$1');
}
