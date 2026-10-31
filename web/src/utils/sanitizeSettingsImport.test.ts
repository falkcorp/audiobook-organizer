// file: web/src/utils/sanitizeSettingsImport.test.ts
// version: 1.3.1
// guid: 8a2d4f60-1b3e-4c7a-b95d-6e0f2a7c3d19
// last-edited: 2026-10-30

import { describe, it, expect } from 'vitest';
import { sanitizeSettingsImport, sanitizeSettingsImportWithReport } from './sanitizeSettingsImport';
import type * as api from '../services/api';

// Legacy settings files carry keys that are no longer on api.Config, so the
// fixtures go through an untyped record the way JSON.parse output does.
const load = (raw: Record<string, unknown>) =>
  sanitizeSettingsImport(raw as Partial<api.Config>);

describe('sanitizeSettingsImport', () => {
  it('folds flat auto_update_* keys into the nested auto_update object', () => {
    const cleaned = load({
      auto_update_enabled: true,
      auto_update_channel: 'beta',
      auto_update_check_minutes: '30',
      auto_update_window_start: 2,
      auto_update_window_end: '4',
    });
    expect(cleaned.auto_update).toEqual({
      enabled: true,
      channel: 'beta',
      check_minutes: 30,
      window_start: 2,
      window_end: 4,
    });
  });

  it('keeps backup_retention_days, coercing a numeric string', () => {
    expect(load({ backup_retention_days: 10 }).backup_retention_days).toBe(10);
    expect(load({ backup_retention_days: '14' }).backup_retention_days).toBe(14);
  });

  it('folds flat maintenance_window_* keys into the nested maintenance object', () => {
    const cleaned = load({
      maintenance_window_enabled: false,
      maintenance_window_start: '1',
      maintenance_window_end: 5,
    });
    expect(cleaned.maintenance).toEqual({ enabled: false, window_start: 1, window_end: 5 });
  });

  it('never sends the flat legacy keys themselves', () => {
    const cleaned = load({
      auto_update_enabled: true,
      maintenance_window_start: 3,
    }) as Record<string, unknown>;
    expect(cleaned).not.toHaveProperty('auto_update_enabled');
    expect(cleaned).not.toHaveProperty('maintenance_window_start');
  });

  it('keeps a nested object from the file and ignores the flat keys beside it', () => {
    const nested = { enabled: false, channel: 'stable', check_minutes: 60, window_start: 0, window_end: 6 };
    const cleaned = load({
      auto_update: nested,
      auto_update_enabled: true,
      auto_update_channel: 'beta',
    });
    expect(cleaned.auto_update).toEqual(nested);
  });

  it('drops non-numeric strings and omits the object when nothing usable is left', () => {
    const cleaned = load({
      maintenance_window_start: 'soon',
      maintenance_window_end: '',
    });
    expect(cleaned).not.toHaveProperty('maintenance');
  });

  it('drops keys the server has no setting for', () => {
    const cleaned = load({
      metadata_llm_scoring_enabled: true,
      env_locked: { root_dir: true },
      activity_db_path: '/elsewhere/activity.db',
      root_dir: '/library',
    }) as Record<string, unknown>;
    expect(cleaned).toEqual({ root_dir: '/library' });
  });

  it('drops the removed auto_fetch_metadata setting from an older export', () => {
    // The server no longer has this setting (it is in removedConfigKeys), so
    // an exported file that still carries it must not send it back.
    const cleaned = load({
      auto_fetch_metadata: true,
      enable_ai_parsing: true,
    }) as Record<string, unknown>;
    expect(cleaned).not.toHaveProperty('auto_fetch_metadata');
    expect(cleaned).toEqual({ enable_ai_parsing: true });
  });

  it('drops the eleven removed unread settings from an older export and keeps the rest', () => {
    // These keys were stored and shown on the Settings page but nothing ever
    // read them. The server refuses a PUT that carries them, so an older export
    // must lose them here while the live settings in the same file still import.
    const cleaned = load({
      metadata_review_default_view: 'compact',
      default_user_quota_gb: 100,
      memory_limit_type: 'items',
      cache_size: 1000,
      memory_limit_percent: 25,
      memory_limit_mb: 512,
      log_format: 'text',
      enable_json_logging: false,
      auth_rate_limit_per_minute: 10,
      language: 'en',
      api_keys: {},
      root_dir: '/library',
      log_level: 'debug',
      concurrent_scans: 4,
      cache_invalidate_on_book_update: true,
    }) as Record<string, unknown>;
    expect(cleaned).toEqual({
      root_dir: '/library',
      log_level: 'debug',
      concurrent_scans: 4,
      cache_invalidate_on_book_update: true,
    });
  });

  it('skips mask-shaped credentials and reports them', () => {
    const { cleaned, skipped } = sanitizeSettingsImportWithReport({
      openai_api_key: 'sk-****cdef',
      metadata_sources: [
        {
          id: 'gotham-books',
          name: 'Gotham Books',
          enabled: true,
          priority: 1,
          requires_auth: true,
          credentials: { apiKey: '********ab', user: 'bruce.wayne' },
        },
      ],
    } as unknown as Partial<api.Config>);
    expect(cleaned.openai_api_key).toBeUndefined();
    expect(cleaned.metadata_sources?.[0].credentials).toEqual({ user: 'bruce.wayne' });
    expect(skipped).toEqual(['openai_api_key', 'metadata_sources[gotham-books].credentials.apiKey']);
  });

  it('keeps a real credential with no asterisk run', () => {
    const { cleaned, skipped } = sanitizeSettingsImportWithReport({
      openai_api_key: 'sk-batman-0001',
    } as unknown as Partial<api.Config>);
    expect(cleaned.openai_api_key).toBe('sk-batman-0001');
    expect(skipped).toEqual([]);
  });
});
