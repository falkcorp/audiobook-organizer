// file: web/src/services/coreIdentity.keyDescription.test.ts
// version: 0.1.0
// guid: af936002-aa4e-4e5f-8649-1fa6f0c5275a
// last-edited: 2026-11-22

// An API key's description crosses the adapter both ways: it goes out on create
// (only when typed) and comes back on the list, the single read and the create answer.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { createAPIKey, getAPIKey, listAPIKeys } from './coreIdentity';
import { coreJson, stubCoreFetch } from '../test/coreFetch';

const coreKey = (extra: Record<string, unknown> = {}) => ({
  id: 'k1',
  owner: 'u1',
  name: 'bat-signal',
  scopes: ['books:view'],
  status: 'active',
  created_at: '2026-11-01T00:00:00Z',
  use_count: 0,
  imported: false,
  ...extra,
});

afterEach(() => vi.unstubAllGlobals());

describe('API key description through the adapter', () => {
  it('list and get carry the description core reports', async () => {
    stubCoreFetch({
      'GET /me/api-keys': coreJson({ items: [coreKey({ description: 'Spare for the Batcave' }), coreKey({ id: 'k2' })] }),
      'GET /api-keys/k1': coreJson(coreKey({ description: 'Spare for the Batcave' })),
    });
    const keys = await listAPIKeys();
    expect(keys[0].description).toBe('Spare for the Batcave');
    // A key without one has no description property at all, not an empty string.
    expect('description' in keys[1]).toBe(false);
    expect((await getAPIKey('k1')).description).toBe('Spare for the Batcave');
  });

  it('create sends the description when one is typed', async () => {
    const { calls } = stubCoreFetch({
      'POST /me/api-keys': coreJson({ key: 'abk_synthetic', api_key: coreKey({ description: 'Alfred phone' }) }, 201),
    });
    await createAPIKey({ name: 'bat-signal', description: 'Alfred phone', scopes: ['books:view'] });
    expect(calls[0].body).toMatchObject({ name: 'bat-signal', description: 'Alfred phone' });
  });

  it('create leaves the field out when there is no description', async () => {
    const { calls } = stubCoreFetch({
      'POST /me/api-keys': coreJson({ key: 'abk_synthetic', api_key: coreKey() }, 201),
    });
    await createAPIKey({ name: 'bat-signal', scopes: ['books:view'] });
    expect(calls[0].body).not.toHaveProperty('description');
  });
});
