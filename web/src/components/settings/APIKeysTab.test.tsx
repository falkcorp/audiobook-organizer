// file: web/src/components/settings/APIKeysTab.test.tsx
// version: 1.0.0
// guid: 7e3a9c51-2b6d-4f80-a1c4-5d9e0b3f7a26
// last-edited: 2026-10-07

import { render, screen, fireEvent, within } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { APIKeysTab } from './APIKeysTab';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return { ...actual, listAPIKeys: vi.fn(), createAPIKey: vi.fn() };
});

const HOUR = 3_600_000;

function key(overrides: Partial<api.APIKey>): api.APIKey {
  return {
    id: 'k1',
    user_id: 'u1',
    name: 'synthetic key',
    description: '',
    scopes: ['library.view'],
    status: 'active',
    created_at: new Date(Date.now() - HOUR).toISOString(),
    last_used_at: new Date(Date.now() - HOUR).toISOString(),
    use_count: 1,
    identifier: 'abk_00000000',
    days_since_last_use: 0,
    never_used: false,
    ...overrides,
  };
}

describe('APIKeysTab expiry', () => {
  beforeEach(() => {
    vi.mocked(api.listAPIKeys).mockReset();
    vi.mocked(api.createAPIKey).mockReset();
  });

  it('shows a future expiry as "in 8h", not a negative "ago"', async () => {
    vi.mocked(api.listAPIKeys).mockResolvedValue([
      key({ expires_at: new Date(Date.now() + 8 * HOUR + 10 * 60_000).toISOString() }),
    ]);
    render(<APIKeysTab />);
    expect(await screen.findByText('in 8h')).toBeInTheDocument();
    expect(screen.queryByText(/-\d+d ago/)).not.toBeInTheDocument();
  });

  it('offers no "Never" expiry when creating a key', async () => {
    vi.mocked(api.listAPIKeys).mockResolvedValue([]);
    render(<APIKeysTab />);
    fireEvent.click(await screen.findByRole('button', { name: /create api key/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.mouseDown(within(dialog).getByRole('combobox'));
    const options = await screen.findAllByRole('option');
    const labels = options.map((o) => o.textContent);
    expect(labels).toContain('365 days (maximum)');
    expect(labels.some((l) => /never/i.test(l ?? ''))).toBe(false);
  });

  it('shows the server note when it shortened the expiry', async () => {
    vi.mocked(api.listAPIKeys).mockResolvedValue([]);
    vi.mocked(api.createAPIKey).mockResolvedValue({
      id: 'k2',
      name: 'child',
      token: 'abk_synthetic',
      scopes: [],
      created_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 2 * HOUR).toISOString(),
      note: 'expiry shortened to the expiry of the API key that created it',
    });
    render(<APIKeysTab />);
    fireEvent.click(await screen.findByRole('button', { name: /create api key/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getAllByRole('textbox')[0], { target: { value: 'child' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^create$/i }));
    expect(await screen.findByText(/expiry shortened/)).toBeInTheDocument();
    expect(screen.getByText('abk_synthetic')).toBeInTheDocument();
  });
});
