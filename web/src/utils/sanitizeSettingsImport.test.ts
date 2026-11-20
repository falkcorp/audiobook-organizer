// file: web/src/utils/sanitizeSettingsImport.test.ts
// version: 1.9.0
// guid: 8a2d4f60-1b3e-4c7a-b95d-6e0f2a7c3d19
// last-edited: 2026-11-19

import { describe, it, expect } from 'vitest';
import {
  IGNORED_ADDRESS_NOTE,
  IGNORED_PROGRAM_NOTE,
  sanitizeSettingsImport,
  sanitizeSettingsImportWithReport,
} from './sanitizeSettingsImport';
import type * as api from '../services/api';

// Legacy settings files carry keys that are no longer on api.Config, so the
// fixtures go through an untyped record the way JSON.parse output does.
const load = (raw: Record<string, unknown>) => sanitizeSettingsImport(raw as Partial<api.Config>);

describe('sanitizeSettingsImport', () => {
  it('folds flat auto_update_* keys into the nested auto_update object', () => {
    const cleaned = load({
      auto_update_enabled: true,
      auto_update_channel: 'beta',
      auto_update_check_minutes: '30',
      auto_update_window_start: 2,
      auto_update_window_end: '4',
    });
    // The window is gone from the server (ignored since the updater hotfix), so
    // the flat keys are dropped with it.
    expect(cleaned.auto_update).toEqual({
      enabled: true,
      channel: 'beta',
      check_minutes: 30,
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
    const nested = { enabled: false, channel: 'stable', check_minutes: 60 };
    const cleaned = load({
      auto_update: nested,
      auto_update_enabled: true,
      auto_update_channel: 'beta',
    });
    expect(cleaned.auto_update).toEqual(nested);
  });

  it('drops the retired update window from a nested auto_update in an older export', () => {
    const cleaned = load({
      auto_update: {
        enabled: true,
        channel: 'stable',
        check_minutes: 60,
        window_start: 0,
        window_end: 6,
      },
    });
    expect(cleaned.auto_update).toEqual({ enabled: true, channel: 'stable', check_minutes: 60 });
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

  it('never imports a secret, masked or not, and reports it', () => {
    const { cleaned, skipped } = sanitizeSettingsImportWithReport({
      openai_api_key: 'sk-batman-0001',
      acoustid_api_key: 'sk-****cdef',
      metadata_sources: [
        {
          id: 'gotham-books',
          name: 'Gotham Books',
          enabled: true,
          priority: 1,
          requires_auth: true,
          credentials: { apiKey: 'sk-gotham-0002', user: 'bruce.wayne' },
        },
      ],
    } as unknown as Partial<api.Config>);
    expect(cleaned).not.toHaveProperty('openai_api_key');
    expect(cleaned).not.toHaveProperty('acoustid_api_key');
    expect(cleaned.metadata_sources?.[0].credentials).toEqual({});
    expect(JSON.stringify(cleaned)).not.toContain('sk-');
    expect(skipped).toEqual([
      'openai_api_key',
      'acoustid_api_key',
      'metadata_sources[gotham-books].credentials.apiKey',
      'metadata_sources[gotham-books].credentials.user',
    ]);
  });

  describe('program paths', () => {
    it('never imports a path the server would execute, and reports each', () => {
      const file = {
        tools: {
          managed_dir: '/tmp/evil',
          mode: 'managed',
          ollama: { mode: 'custom', custom_path: '/tmp/evil/ollama' },
          fpcalc: { mode: 'custom', custom_path: '/tmp/evil/fpcalc' },
        },
      } as unknown as Partial<api.Config>;
      const copy = JSON.stringify(file);
      const { cleaned, ignored } = sanitizeSettingsImportWithReport(file, undefined);
      expect(JSON.stringify(cleaned)).not.toContain('/tmp/evil');
      expect(cleaned.tools).toEqual({
        mode: 'managed',
        ollama: { mode: 'custom' },
        fpcalc: { mode: 'custom' },
      });
      expect(ignored).toEqual([
        `tools.managed_dir (${IGNORED_PROGRAM_NOTE})`,
        `tools.ollama.custom_path (${IGNORED_PROGRAM_NOTE})`,
        `tools.fpcalc.custom_path (${IGNORED_PROGRAM_NOTE})`,
      ]);
      expect(JSON.stringify(file)).toBe(copy); // the caller's object is untouched
    });

    it('drops an empty path too (the file never sets one) and stays quiet without any', () => {
      const withEmpty = sanitizeSettingsImportWithReport(
        { tools: { fpcalc: { custom_path: '' } } } as unknown as Partial<api.Config>,
        undefined
      );
      expect(withEmpty.ignored).toEqual([`tools.fpcalc.custom_path (${IGNORED_PROGRAM_NOTE})`]);
      const without = sanitizeSettingsImportWithReport(
        { tools: { allow_periodic_ollama: true } } as unknown as Partial<api.Config>,
        undefined
      );
      expect(without.cleaned.tools).toEqual({ allow_periodic_ollama: true });
      expect(without.ignored).toEqual([]);
    });
  });

  describe('addresses a stored credential is sent to', () => {
    const MIRROR = 'https://books.mirror.example.test';
    const ATTACKER = 'https://evil.example.test';
    const current = {
      metadata_sources: [{ id: 'hardcover', base_url: MIRROR }, { id: 'audible' }],
      embedding: { base_url: 'https://embed.example.test' },
    };
    const fileWith = (sources: unknown[]) =>
      ({ metadata_sources: sources }) as unknown as Partial<api.Config>;
    const src = (id: string, more: Record<string, unknown> = {}) => ({
      id,
      name: id,
      enabled: true,
      priority: 1,
      requires_auth: true,
      credentials: { apiKey: '' },
      ...more,
    });

    it("drops an imported base_url on an existing source, keeps the server's, and reports it", () => {
      const { cleaned, ignored } = sanitizeSettingsImportWithReport(
        fileWith([src('hardcover', { base_url: ATTACKER })]),
        current
      );
      expect(cleaned.metadata_sources?.[0]).toMatchObject({ base_url: MIRROR });
      expect(JSON.stringify(cleaned)).not.toContain(ATTACKER);
      expect(ignored).toEqual([`metadata_sources[hardcover].base_url (${IGNORED_ADDRESS_NOTE})`]);
    });

    it('does not give an existing source with no saved address one from the file', () => {
      const { cleaned, ignored } = sanitizeSettingsImportWithReport(
        fileWith([src('audible', { base_url: ATTACKER })]),
        current
      );
      expect(cleaned.metadata_sources?.[0]).not.toHaveProperty('base_url');
      expect(ignored).toHaveLength(1);
    });

    it('reports nothing when the file repeats the saved address', () => {
      const { cleaned, ignored } = sanitizeSettingsImportWithReport(
        fileWith([src('hardcover', { base_url: MIRROR })]),
        current
      );
      expect(cleaned.metadata_sources?.[0]).toMatchObject({ base_url: MIRROR });
      expect(ignored).toEqual([]);
    });

    it.each(['google-books', 'hardcover', 'audible', 'audnexus', 'openlibrary', 'gotham-books'])(
      'imports a new source %s without its base_url and without credentials',
      (id) => {
        const { cleaned, ignored } = sanitizeSettingsImportWithReport(
          fileWith([src(id, { base_url: ATTACKER, credentials: { apiKey: 'sk-gotham-0003' } })]),
          // The server has none of them yet.
          { metadata_sources: [{ id: 'unrelated' }] }
        );
        const added = cleaned.metadata_sources?.[0] as unknown as Record<string, unknown>;
        expect(added).not.toHaveProperty('base_url');
        expect(added.credentials).toEqual({});
        expect(JSON.stringify(cleaned)).not.toContain(ATTACKER);
        expect(JSON.stringify(cleaned)).not.toContain('sk-gotham-0003');
        expect(ignored).toEqual([`metadata_sources[${id}].base_url (${IGNORED_ADDRESS_NOTE})`]);
      }
    );

    it('does not match a source by a look-alike id', () => {
      const { cleaned } = sanitizeSettingsImportWithReport(
        fileWith([src('Hardcover', { base_url: ATTACKER })]),
        current
      );
      expect(cleaned.metadata_sources?.[0]).not.toHaveProperty('base_url');
    });

    it('imports no address at all when the current config could not be read', () => {
      const { cleaned, ignored } = sanitizeSettingsImportWithReport(
        fileWith([src('hardcover', { base_url: ATTACKER })])
      );
      expect(cleaned.metadata_sources?.[0]).not.toHaveProperty('base_url');
      expect(ignored).toHaveLength(1);
    });

    it('keeps rate_limit, and reports no blank credential from an exported file', () => {
      const { cleaned, skipped } = sanitizeSettingsImportWithReport(
        fileWith([src('hardcover', { base_url: MIRROR, rate_limit: { tier: 'low' } })]),
        current
      );
      expect(cleaned.metadata_sources?.[0]).toMatchObject({
        rate_limit: { tier: 'low' },
        credentials: {},
      });
      expect(skipped).toEqual([]);
    });

    it('never imports embedding.base_url (the embeddings client sends the OpenAI key there)', () => {
      const { cleaned, ignored } = sanitizeSettingsImportWithReport(
        { embedding: { enabled: true, base_url: ATTACKER } } as unknown as Partial<api.Config>,
        current
      );
      expect(cleaned.embedding).toEqual({ enabled: true });
      expect(JSON.stringify(cleaned)).not.toContain(ATTACKER);
      expect(ignored).toEqual([`embedding.base_url (${IGNORED_ADDRESS_NOTE})`]);
    });

    it('stays quiet when embedding.base_url matches the server', () => {
      const { cleaned, ignored } = sanitizeSettingsImportWithReport(
        {
          embedding: { enabled: true, base_url: 'https://embed.example.test' },
        } as unknown as Partial<api.Config>,
        current
      );
      expect(cleaned.embedding).toEqual({ enabled: true });
      expect(ignored).toEqual([]);
    });

    it('has no other importable section that carries an address', () => {
      // Deny-by-default: these hold a URL next to a stored secret, and none is
      // importable at all.
      const { cleaned } = sanitizeSettingsImportWithReport({
        openai_base_url: ATTACKER,
        ai_backend: { local_base_url: ATTACKER },
        ai_endpoints: [{ id: 'x', base_url: ATTACKER }],
        deluge_web_url: ATTACKER,
        download_client: { torrent: { deluge: { host: ATTACKER } } },
        whisper_remote_url: ATTACKER,
        public_base_url: ATTACKER,
        oauth_redirect_base_url: ATTACKER,
        plugins: { webhook: { settings: { url: ATTACKER } } },
      } as unknown as Partial<api.Config>);
      expect(JSON.stringify(cleaned)).not.toContain(ATTACKER);
    });
  });

  it('reports nothing when the file carries no secret', () => {
    const { skipped } = sanitizeSettingsImportWithReport({ root_dir: '/library' });
    expect(skipped).toEqual([]);
  });
});
