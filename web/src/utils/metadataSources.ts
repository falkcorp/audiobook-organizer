// file: web/src/utils/metadataSources.ts
// version: 1.0.0
// guid: 9a4e6d12-5b73-4c08-a1f9-3e7d0b2c8a64
// last-edited: 2026-11-19

import type * as api from '../services/api';

/**
 * The metadata-source list the Settings page edits, and the translation to and
 * from what the server serves for the `metadata_sources` key.
 *
 * Credentials are write-only. The server serves each source's credential keys
 * with blank values (a key's presence means one is saved) and reads a write as:
 * a non-empty string replaces the saved one, an empty string or an absent key
 * leaves it, `null` deletes it. So the page holds only what the user typed, and
 * says which credentials are saved from the key names alone.
 */
export interface UiMetadataSource {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  requiresAuth: boolean;
  /** What the user typed this session: never a saved value. */
  credentials: { [key: string]: string };
  /** Credential names the server holds a value for. */
  savedCredentials: string[];
  /** Saved credentials the user staged for deletion on the next save. */
  clearCredentials: string[];
  /**
   * Fields of the source the page has no control for (base_url, rate_limit ...),
   * sent back unchanged: a save names the whole list, so dropping them here
   * would erase them on the server.
   */
  extra: { [key: string]: unknown };
}

/** Sources whose API key the page always offers a field for. */
export const AUTH_SOURCES = ['google-books', 'hardcover'];

const PAGE_FIELDS = new Set(['id', 'name', 'enabled', 'priority', 'requires_auth', 'credentials']);

const source = (
  id: string,
  name: string,
  enabled: boolean,
  priority: number,
  requiresAuth: boolean
): UiMetadataSource => ({
  id,
  name,
  enabled,
  priority,
  requiresAuth,
  credentials: {},
  savedCredentials: [],
  clearCredentials: [],
  extra: {},
});

/** The list a fresh install starts from, in priority order. */
export const defaultMetadataSources = (): UiMetadataSource[] => [
  source('audible', 'Audible', true, 1, false),
  source('openlibrary', 'Open Library', true, 2, false),
  source('audnexus', 'Audnexus', true, 3, false),
  source('google-books', 'Google Books', false, 4, true),
  source('hardcover', 'Hardcover', false, 5, true),
];

/** The page's list from the served `metadata_sources`; the defaults when none is stored. */
export function uiSourcesFromConfig(sources: api.MetadataSource[] | undefined): UiMetadataSource[] {
  if (!sources || sources.length === 0) return defaultMetadataSources();
  return sources.map((s) => ({
    id: s.id,
    name: s.name,
    enabled: s.enabled,
    priority: s.priority,
    // Sources that need an API key stay marked so, even when the stored entry
    // says requires_auth: false.
    requiresAuth: s.requires_auth || AUTH_SOURCES.includes(s.id),
    credentials: {},
    savedCredentials: Object.keys(s.credentials ?? {}),
    clearCredentials: [],
    extra: Object.fromEntries(
      Object.entries(s as unknown as Record<string, unknown>).filter(([k]) => !PAGE_FIELDS.has(k))
    ),
  }));
}

/** What to PUT for the page's list. A credential not typed or cleared goes as blank, which leaves it. */
export function sourcesPayload(sources: UiMetadataSource[]): api.MetadataSourceWrite[] {
  return sources.map((s) => {
    const credentials: { [key: string]: string | null } = {};
    for (const name of s.savedCredentials) credentials[name] = '';
    for (const [name, typed] of Object.entries(s.credentials)) {
      if (typed !== '') credentials[name] = typed;
    }
    for (const name of s.clearCredentials) credentials[name] = null;
    return {
      ...s.extra,
      id: s.id,
      name: s.name,
      enabled: s.enabled,
      priority: s.priority,
      requires_auth: s.requiresAuth,
      credentials,
    };
  });
}

/** True when the source has a credential that is saved and not staged for deletion. */
export function hasSavedCredential(s: UiMetadataSource, name: string): boolean {
  return s.savedCredentials.includes(name) && !s.clearCredentials.includes(name);
}
