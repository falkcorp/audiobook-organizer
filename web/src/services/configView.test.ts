// file: web/src/services/configView.test.ts
// version: 1.0.0
// guid: 5e9a3c71-2d84-4f06-b7a1-8c0d6e2f4b39
// last-edited: 2026-11-19

import { describe, expect, it } from 'vitest';
import {
  buildUpdateBody,
  canonicalJSON,
  diffUpdate,
  parseView,
  type ConfigView,
} from './configView';

const view: ConfigView = {
  generation: 7,
  keys: [
    { name: 'root_dir', class: 'i', value: '/library/gotham', admin: true },
    { name: 'log_level', class: 'k', value: 'info' },
    { name: 'concurrent_scans', class: 'k', value: 4 },
    { name: 'auto_update.enabled', class: 'k', value: false },
    { name: 'auto_update.channel', class: 'k', value: 'stable' },
    { name: 'database_path', class: 'k', value: '/var/lib/abo/db', locked_by: 'flag --db' },
    { name: 'supported_extensions', class: 'k', value: ['.m4b', '.mp3'] },
    {
      name: 'openai_api_key',
      class: 's',
      secret: { set: true, source: 'stored', fingerprint: 'ab12cd34' },
    },
    { name: 'acoustid_api_key', class: 's', secret: { set: false } },
  ],
};

describe('parseView', () => {
  const parsed = parseView(view);

  it('nests dotted key names into the object the page edits', () => {
    expect(parsed.config).toMatchObject({
      root_dir: '/library/gotham',
      log_level: 'info',
      auto_update: { enabled: false, channel: 'stable' },
    });
  });

  it('keeps a secret out of the config and reports its status instead', () => {
    expect(parsed.config).not.toHaveProperty('openai_api_key');
    expect(parsed.secretStatus.openai_api_key).toEqual({
      set: true,
      source: 'stored',
      fingerprint: 'ab12cd34',
    });
    expect(parsed.secretStatus.acoustid_api_key).toEqual({ set: false });
    expect(parsed.secretNames).toEqual(new Set(['openai_api_key', 'acoustid_api_key']));
  });

  it('derives the lock maps from locked_by', () => {
    expect(parsed.envLocked).toEqual(['database_path']);
    expect(parsed.settingLocks).toEqual({ database_path: 'flag --db' });
  });

  it('records every served name, secrets included, and a value baseline for the plain ones', () => {
    expect(parsed.known.has('openai_api_key')).toBe(true);
    expect(parsed.baseline.has('openai_api_key')).toBe(false);
    expect(parsed.baseline.get('auto_update.channel')).toBe('stable');
  });

  it('treats a missing key list as an empty view', () => {
    expect(parseView({ generation: 0 } as unknown as ConfigView).config).toEqual({});
  });
});

describe('diffUpdate', () => {
  const { known, baseline } = parseView(view);

  it('names only the keys whose value moved', () => {
    const set = diffUpdate(
      {
        root_dir: '/library/gotham',
        log_level: 'debug',
        concurrent_scans: 4,
        auto_update: { enabled: true, channel: 'stable' },
      },
      known,
      baseline
    );
    expect(set).toEqual({ log_level: 'debug', 'auto_update.enabled': true });
  });

  it('never names a locked key whose value is unchanged', () => {
    expect(diffUpdate({ database_path: '/var/lib/abo/db' }, known, baseline)).toEqual({});
  });

  it('names a locked key when it was edited, so the server refuses it by name', () => {
    expect(diffUpdate({ database_path: '/elsewhere' }, known, baseline)).toEqual({
      database_path: '/elsewhere',
    });
  });

  it('sends an array as one whole value', () => {
    expect(diffUpdate({ supported_extensions: ['.m4b'] }, known, baseline)).toEqual({
      supported_extensions: ['.m4b'],
    });
    expect(diffUpdate({ supported_extensions: ['.m4b', '.mp3'] }, known, baseline)).toEqual({});
  });

  it('sends a key the server does not serve, so the refusal names it', () => {
    expect(diffUpdate({ batcave_mode: true }, known, baseline)).toEqual({ batcave_mode: true });
  });

  it('ignores undefined values', () => {
    expect(diffUpdate({ log_level: undefined }, known, baseline)).toEqual({});
  });

  it('compares objects regardless of key order', () => {
    expect(canonicalJSON({ b: 1, a: { d: 2, c: 3 } })).toBe(canonicalJSON({ a: { c: 3, d: 2 }, b: 1 }));
  });
});

describe('buildUpdateBody', () => {
  it('puts a typed secret in set beside the changed keys', () => {
    expect(
      buildUpdateBody({ log_level: 'debug' }, { secrets: { openai_api_key: 'sk-robin-0003' } })
    ).toEqual({ set: { log_level: 'debug', openai_api_key: 'sk-robin-0003' }, unset: [] });
  });

  it('treats a blank secret as "leave it", never as a clear', () => {
    expect(buildUpdateBody({}, { secrets: { openai_api_key: '' } })).toEqual({ set: {}, unset: [] });
  });

  it('clears a secret through unset', () => {
    expect(buildUpdateBody({}, { clear_secrets: ['openai_api_key'] })).toEqual({
      set: {},
      unset: ['openai_api_key'],
    });
  });

  it('lets a clear win over a typed value for the same secret', () => {
    expect(
      buildUpdateBody({}, { secrets: { openai_api_key: 'sk-robin-0003' }, clear_secrets: ['openai_api_key'] })
    ).toEqual({ set: {}, unset: ['openai_api_key'] });
  });
});
