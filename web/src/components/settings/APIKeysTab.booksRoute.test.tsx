// file: web/src/components/settings/APIKeysTab.booksRoute.test.tsx
// version: 0.1.0
// guid: 8d77ba94-3712-4faa-93e0-fa74b6596b7c
// last-edited: 2026-11-22

// The Settings API Keys tab end to end over a stubbed fetch (no mock of services/api): the
// picker offers HEAD's atom names, Create posts to the books key route with the description,
// the token is shown once, and the list shows the description (F4-08 B10sk).

import { render, screen, fireEvent, within, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { APIKeysTab } from './APIKeysTab';

const HEAD_ATOMS = [
  'library.view',
  'library.edit_metadata',
  'library.delete',
  'library.organize',
  'scan.trigger',
  'integrations.manage',
  'users.manage',
  'settings.manage',
  'playlists.create',
  'requests.create',
  'requests.approve',
  'fingerprint.worker',
];

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const headKey = (extra: Record<string, unknown> = {}) => ({
  id: 'k1',
  user_id: 'u1',
  name: 'bat-signal',
  scopes: ['library.view'],
  status: 'active',
  created_at: new Date(Date.now() - 3_600_000).toISOString(),
  use_count: 0,
  identifier: 'abk_0123abcd',
  days_since_last_use: null,
  never_used: true,
  ...extra,
});

const mockFetch = vi.fn();
const calls = () =>
  mockFetch.mock.calls.map(([url, init]) => ({
    url: String(url),
    method: (init as RequestInit | undefined)?.method ?? 'GET',
    body: (init as RequestInit | undefined)?.body ? JSON.parse(String((init as RequestInit).body)) : undefined,
  }));

describe('API Keys tab talks to the books key route', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
    let created = false;
    mockFetch.mockImplementation(async (url: unknown, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      if (method === 'POST' && String(url) === '/api/v1/auth/api-keys') {
        created = true;
        return json(
          { data: { id: 'k1', name: 'bat-signal', token: 'abk_synthetic_once', scopes: ['library.view'], created_at: 'x' } },
          201
        );
      }
      if (method === 'GET' && String(url) === '/api/v1/auth/api-keys') {
        return json({ data: { api_keys: created ? [headKey({ description: 'Alfred phone' })] : [], count: 0 } });
      }
      return json({ error: 'stub has no ' + method + ' ' + String(url) }, 404);
    });
  });
  afterEach(() => mockFetch.mockReset());

  it('offers HEAD\'s atom names, not core permission names, in the scope picker', async () => {
    render(<APIKeysTab />);
    fireEvent.click(await screen.findByRole('button', { name: /create api key/i }));
    const dialog = await screen.findByRole('dialog');
    const offered = within(dialog)
      .getAllByRole('checkbox')
      .map((box) => box.closest('label')?.textContent ?? '');
    expect(offered).toEqual(HEAD_ATOMS);
    expect(offered.some((s) => s.includes(':'))).toBe(false);
  });

  it('create posts the name, description, atom scopes and expiry to /api/v1/auth/api-keys, shows the token once, then lists the description', async () => {
    render(<APIKeysTab />);
    fireEvent.click(await screen.findByRole('button', { name: /create api key/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/^name/i), { target: { value: 'bat-signal' } });
    fireEvent.change(within(dialog).getByLabelText('Description'), { target: { value: ' Alfred phone ' } });
    fireEvent.click(within(dialog).getByRole('checkbox', { name: 'library.view' }));
    fireEvent.click(within(dialog).getByRole('button', { name: /^create$/i }));

    expect(await screen.findByText('abk_synthetic_once')).toBeInTheDocument();
    const post = calls().find((c) => c.method === 'POST');
    expect(post).toEqual({
      url: '/api/v1/auth/api-keys',
      method: 'POST',
      body: { name: 'bat-signal', description: 'Alfred phone', scopes: ['library.view'], expires_in_days: 90 },
    });

    // Once: closing the dialog drops the token, and the list never carries it.
    fireEvent.click(screen.getByRole('button', { name: /i've saved this, close/i }));
    await waitFor(() => expect(screen.queryByText('abk_synthetic_once')).not.toBeInTheDocument());

    const name = await screen.findByText('bat-signal');
    fireEvent.mouseOver(name);
    expect(await screen.findByRole('tooltip')).toHaveTextContent('Alfred phone');
    expect(screen.queryByText('abk_synthetic_once')).not.toBeInTheDocument();
    // Every request of the flow went to the books route.
    for (const c of calls()) expect(c.url).toBe('/api/v1/auth/api-keys');
  });
});
