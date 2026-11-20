// file: web/src/utils/metadataSources.test.ts
// version: 1.0.0
// guid: 3d8b5f27-6a41-4e90-b2c8-7f1a9d4e0c53
// last-edited: 2026-11-19

import { describe, expect, it } from 'vitest';
import type * as api from '../services/api';
import {
  defaultMetadataSources,
  hasSavedCredential,
  sourcesPayload,
  uiSourcesFromConfig,
} from './metadataSources';

const served: api.MetadataSource[] = [
  {
    id: 'google-books',
    name: 'Google Books',
    enabled: true,
    priority: 1,
    requires_auth: true,
    // A saved credential is served as its key with a blank value.
    credentials: { apiKey: '' },
    base_url: 'https://books.mirror.example.test',
    rate_limit: { tier: 'low' },
  } as unknown as api.MetadataSource,
  {
    id: 'audible',
    name: 'Audible',
    enabled: true,
    priority: 2,
    requires_auth: false,
    credentials: {},
  },
];

describe('uiSourcesFromConfig', () => {
  it('starts from the defaults when nothing is stored', () => {
    expect(uiSourcesFromConfig(undefined).map((s) => s.id)).toEqual([
      'audible',
      'openlibrary',
      'audnexus',
      'google-books',
      'hardcover',
    ]);
    expect(uiSourcesFromConfig([])).toEqual(defaultMetadataSources());
  });

  it('reads a saved credential from the key being present, and holds no value for it', () => {
    const [google, audible] = uiSourcesFromConfig(served);
    expect(google.savedCredentials).toEqual(['apiKey']);
    expect(google.credentials).toEqual({});
    expect(hasSavedCredential(google, 'apiKey')).toBe(true);
    expect(audible.savedCredentials).toEqual([]);
    expect(hasSavedCredential(audible, 'apiKey')).toBe(false);
  });

  it('keeps the fields the page has no control for', () => {
    const [google] = uiSourcesFromConfig(served);
    expect(google.extra).toEqual({
      base_url: 'https://books.mirror.example.test',
      rate_limit: { tier: 'low' },
    });
  });

  it('keeps an API-key source marked as needing auth even when the stored entry says not', () => {
    const [s] = uiSourcesFromConfig([
      {
        id: 'hardcover',
        name: 'Hardcover',
        enabled: false,
        priority: 1,
        requires_auth: false,
        credentials: {},
      },
    ]);
    expect(s.requiresAuth).toBe(true);
  });
});

describe('sourcesPayload', () => {
  it('sends an untouched list exactly as served, so a save of it changes nothing', () => {
    const payload = sourcesPayload(uiSourcesFromConfig(served));
    expect(payload).toEqual(served);
  });

  it('replaces a saved credential with the typed one', () => {
    const ui = uiSourcesFromConfig(served);
    ui[0].credentials = { apiKey: 'sk-penguin-0009' };
    expect(sourcesPayload(ui)[0].credentials).toEqual({ apiKey: 'sk-penguin-0009' });
  });

  it('sends a typed credential for a source that had none', () => {
    const ui = uiSourcesFromConfig(served);
    ui[1].credentials = { apiKey: 'sk-penguin-0010' };
    expect(sourcesPayload(ui)[1].credentials).toEqual({ apiKey: 'sk-penguin-0010' });
  });

  it('deletes a staged credential with null, not an empty string', () => {
    const ui = uiSourcesFromConfig(served);
    ui[0].clearCredentials = ['apiKey'];
    expect(sourcesPayload(ui)[0].credentials).toEqual({ apiKey: null });
    expect(hasSavedCredential(ui[0], 'apiKey')).toBe(false);
  });

  it('never invents a credential for a field the user left blank', () => {
    const ui = uiSourcesFromConfig(served);
    ui[1].credentials = { apiKey: '' };
    expect(sourcesPayload(ui)[1].credentials).toEqual({});
  });
});
