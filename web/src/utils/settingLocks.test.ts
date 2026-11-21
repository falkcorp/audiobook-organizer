// file: web/src/utils/settingLocks.test.ts
// version: 1.0.0
// guid: 0a5c8e13-7d42-4b96-8f31-6e2b9d1c4a70
// last-edited: 2026-11-19

import { describe, expect, it } from 'vitest';
import { describeLock } from './settingLocks';

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
