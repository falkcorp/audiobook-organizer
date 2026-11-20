// file: web/src/services/api.config.test.ts
// version: 1.0.0
// guid: 2c7f4a90-8b13-4d65-9e02-a5d1c3b6e847
// last-edited: 2026-11-19
//
// getConfig/updateConfig over the key-shaped /config: secrets are write-only
// and a save names only the keys that changed.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getConfig, getConfigView, updateConfig } from './api';
import type { ConfigView } from './configView';

const SENTINEL = 'sk-joker-sentinel-0042';

function viewWith(overrides: Partial<Record<string, unknown>> = {}): ConfigView {
  return {
    generation: 3,
    naming_pattern_tokens: ['{title}', '{author}'],
    keys: [
      { name: 'log_level', class: 'k', value: overrides.log_level ?? 'info' },
      { name: 'root_dir', class: 'i', value: '/library/gotham', admin: true },
      { name: 'database_path', class: 'k', value: '/var/lib/abo/db', locked_by: 'flag --db' },
      { name: 'auto_update.enabled', class: 'k', value: false },
      {
        name: 'openai_api_key',
        class: 's',
        secret: { set: overrides.keySet !== false, source: 'stored', fingerprint: 'ab12cd34' },
      },
    ],
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('config over the key-shaped API', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  // The module keeps the last read as the diff baseline; each test starts a
  // minute later than the last so none inherits a fresh one.
  let clock = new Date('2026-11-19T12:00:00Z').getTime();
  beforeEach(() => {
    clock += 60_000;
    vi.setSystemTime(clock);
    fetchMock = vi.fn();
    global.fetch = fetchMock as unknown as typeof fetch;
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  const lastPut = () => {
    const call = fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PUT');
    expect(call).toBeDefined();
    return JSON.parse(String((call![1] as RequestInit).body));
  };

  it('getConfig folds the keys into the nested config the page edits', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    const cfg = await getConfig();
    expect(cfg.root_dir).toBe('/library/gotham');
    expect(cfg.auto_update?.enabled).toBe(false);
    expect(cfg.naming_pattern_tokens).toEqual(['{title}', '{author}']);
    expect(cfg.env_locked).toEqual(['database_path']);
    expect(cfg.setting_locks).toEqual({ database_path: 'flag --db' });
  });

  it('getConfig carries a secret as status only, with no value and no mask', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    const cfg = await getConfig();
    expect(cfg.secret_status?.openai_api_key).toEqual({
      set: true,
      source: 'stored',
      fingerprint: 'ab12cd34',
    });
    expect(cfg).not.toHaveProperty('openai_api_key');
    expect(JSON.stringify(cfg)).not.toContain('***');
  });

  it('getConfigView returns the keys as served', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    const v = await getConfigView();
    expect(v.generation).toBe(3);
    expect(v.keys.map((k) => k.name)).toContain('openai_api_key');
  });

  it('updateConfig sends only the changed keys, in set', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    await getConfig();
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith({ log_level: 'debug' }) }));
    const next = await updateConfig({
      log_level: 'debug',
      root_dir: '/library/gotham',
      database_path: '/var/lib/abo/db',
      auto_update: { enabled: false },
    });
    expect(lastPut()).toEqual({ set: { log_level: 'debug' }, unset: [] });
    expect(next.log_level).toBe('debug');
  });

  it('updateConfig stores a typed secret and the response never echoes it', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith({ keySet: false }) }));
    const before = await getConfig();
    expect(before.secret_status?.openai_api_key?.set).toBe(false);
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    const after = await updateConfig({ secrets: { openai_api_key: SENTINEL } });
    expect(lastPut()).toEqual({ set: { openai_api_key: SENTINEL }, unset: [] });
    expect(after.secret_status?.openai_api_key?.set).toBe(true);
    expect(JSON.stringify(after)).not.toContain(SENTINEL);
  });

  it('updateConfig clears a secret with unset', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    await getConfig();
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith({ keySet: false }) }));
    const after = await updateConfig({ clear_secrets: ['openai_api_key'] });
    expect(lastPut()).toEqual({ set: {}, unset: ['openai_api_key'] });
    expect(after.secret_status?.openai_api_key?.set).toBe(false);
  });

  it('updateConfig makes no request when nothing differs, since the server refuses an empty patch', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    await getConfig();
    const cfg = await updateConfig({ log_level: 'info', secrets: { openai_api_key: '' } });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(cfg.log_level).toBe('info');
  });

  it('updateConfig reads the config first when none was read yet', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith({ log_level: 'debug' }) }));
    await updateConfig({ log_level: 'debug' });
    expect(fetchMock.mock.calls[0][0]).toContain('/config');
    expect((fetchMock.mock.calls[0][1] as RequestInit | undefined)?.method ?? 'GET').toBe('GET');
    expect(lastPut()).toEqual({ set: { log_level: 'debug' }, unset: [] });
  });

  it('updateConfig re-reads a baseline older than the ttl, so a stale one cannot drop an edit', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    await getConfig();
    // Another session saved debug; this page still believes info.
    vi.setSystemTime(clock + 30_000);
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith({ log_level: 'debug' }) }));
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith({ log_level: 'info' }) }));
    await updateConfig({ log_level: 'info' });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(lastPut()).toEqual({ set: { log_level: 'info' }, unset: [] });
  });

  it('updateConfig diffs against the server\'s answer to the last save', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    await getConfig();
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith({ log_level: 'debug' }) }));
    await updateConfig({ log_level: 'debug' });
    // Saving debug again is now a no-op: the baseline moved to what the server returned.
    await updateConfig({ log_level: 'debug' });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('a refusal throws an ApiError carrying the server\'s message and status', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: viewWith() }));
    await getConfig();
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: 'database_path is set by flag --db and cannot be changed here' }, 409)
    );
    await expect(updateConfig({ database_path: '/elsewhere' })).rejects.toMatchObject({
      name: 'ApiError',
      status: 409,
      message: expect.stringContaining('database_path'),
    });
  });
});
