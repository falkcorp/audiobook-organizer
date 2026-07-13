// file: web/src/utils/tagDisplay.ts
// version: 1.0.0
// guid: d638b9b0-4403-4fe1-acdd-05cc951128d0
// last-edited: 2026-07-13

// Display-only formatting for `metadata:*` system tags. Storage and the API
// keep the raw namespaced tag strings (e.g. `metadata:language:en`,
// `metadata:source:audible`) exactly as-is — these helpers only change what
// the UI shows. Never feed the *output* of formatTagLabel back into a filter
// value or API call; always use the original tag string for that.

const METADATA_PREFIX = 'metadata:';
const LANGUAGE_PREFIX = 'metadata:language:';
const SOURCE_PREFIX = 'metadata:source:';

// languageDisplayNames lazily resolves ISO 639-1/639-2 codes to English
// language names. Constructed once and reused; Intl.DisplayNames throws in
// environments without full ICU data, so callers must still guard with
// try/catch (see languageNameFor).
let languageDisplayNames: Intl.DisplayNames | null | undefined;

function getLanguageDisplayNames(): Intl.DisplayNames | null {
  if (languageDisplayNames === undefined) {
    try {
      languageDisplayNames = new Intl.DisplayNames(['en'], { type: 'language' });
    } catch {
      languageDisplayNames = null;
    }
  }
  return languageDisplayNames;
}

/**
 * languageNameFor resolves a language code (e.g. "en", "spa") to its English
 * display name (e.g. "English", "Spanish"). Falls back to the code
 * uppercased if `Intl.DisplayNames` is unavailable, throws, or returns the
 * code unchanged (its own "I don't recognize this" signal).
 */
export function languageNameFor(code: string): string {
  const trimmed = code.trim();
  if (!trimmed) return trimmed;
  const displayNames = getLanguageDisplayNames();
  if (displayNames) {
    try {
      const name = displayNames.of(trimmed);
      if (name && name.toLowerCase() !== trimmed.toLowerCase()) {
        return name;
      }
    } catch {
      // fall through to the uppercase fallback below
    }
  }
  return trimmed.toUpperCase();
}

// SOURCE_LABEL_OVERRIDES covers source names whose title-cased form isn't
// the natural display name (multi-word product names, acronyms, etc.).
const SOURCE_LABEL_OVERRIDES: Record<string, string> = {
  openlibrary: 'Open Library',
  googlebooks: 'Google Books',
  audnexus: 'Audnexus',
};

function titleCase(value: string): string {
  return value
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');
}

/**
 * sourceNameFor resolves a `metadata:source:<name>` suffix to a clean
 * display label, e.g. "audible" -> "Audible", "openlibrary" -> "Open
 * Library". Falls back to a generic title-case for unmapped sources.
 */
export function sourceNameFor(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return trimmed;
  const override = SOURCE_LABEL_OVERRIDES[trimmed.toLowerCase()];
  if (override) return override;
  return titleCase(trimmed);
}

/**
 * isSourceTag reports whether `tag` is a `metadata:source:*` system tag.
 * These are hidden from chip rendering on book cards / book detail — they
 * remain filter-only (see FilterSidebar).
 */
export function isSourceTag(tag: string): boolean {
  return tag.startsWith(SOURCE_PREFIX) && tag.length > SOURCE_PREFIX.length;
}

/**
 * shouldRenderTagChip reports whether `tag` should render as a chip on
 * book cards / book detail. Source tags are filter-only and never render
 * as chips there.
 */
export function shouldRenderTagChip(tag: string): boolean {
  return !isSourceTag(tag);
}

/**
 * formatTagLabel returns the display label for `tag`:
 *  - `metadata:language:<code>` -> the language name (e.g. "English").
 *  - `metadata:source:<name>` -> a clean source label (e.g. "Audible"),
 *    used only for the FilterSidebar facet — callers that render chips on
 *    book cards/detail should skip these via `shouldRenderTagChip` first.
 *  - any other `metadata:*` tag -> the tag with the `metadata:` prefix
 *    stripped.
 *  - any non-`metadata:` tag -> returned unchanged.
 *
 * The raw tag string is always what's sent to the API/filters — this
 * function is display-only.
 */
export function formatTagLabel(tag: string): string {
  if (tag.startsWith(LANGUAGE_PREFIX)) {
    return languageNameFor(tag.slice(LANGUAGE_PREFIX.length));
  }
  if (tag.startsWith(SOURCE_PREFIX)) {
    return sourceNameFor(tag.slice(SOURCE_PREFIX.length));
  }
  if (tag.startsWith(METADATA_PREFIX)) {
    return tag.slice(METADATA_PREFIX.length);
  }
  return tag;
}
