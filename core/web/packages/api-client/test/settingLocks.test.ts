// file: core/web/packages/api-client/test/settingLocks.test.ts
// version: 0.1.0
// guid: dff10a1a-1f04-4c0b-a591-530efadf9b45
// last-edited: 2026-11-22

import { describe, expect, it } from 'vitest';
import { describeLock } from '../src/runtime/settingLocks';

describe('describeLock', () => {
  it('names an environment variable', () => {
    expect(describeLock('env ABO_CORE_LOG_LEVEL')).toBe(
      'the ABO_CORE_LOG_LEVEL environment variable on the server'
    );
  });

  it('names a flag as a flag, not a variable', () => {
    expect(describeLock('flag --db')).toBe("the --db flag on the server's start-up command");
  });

  it('names the config file and the path the server read', () => {
    expect(describeLock('file /etc/abo/config.yaml')).toBe(
      "the server's config file (/etc/abo/config.yaml)"
    );
    expect(describeLock('file')).toBe("the server's config file");
  });

  it('falls back to generic copy when nothing is known', () => {
    expect(describeLock(undefined)).toBe('the server configuration');
    expect(describeLock('')).toBe('the server configuration');
  });

  it('passes an unknown layer through rather than inventing a place to look', () => {
    expect(describeLock('vault secret/abo')).toBe('vault secret/abo');
  });
});
