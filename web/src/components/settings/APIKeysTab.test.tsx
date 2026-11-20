// file: web/src/components/settings/APIKeysTab.test.tsx
// version: 1.0.0
// guid: 7e3a9c51-2b6d-4f80-a1c4-5d9e0b3f7a26
// last-edited: 2026-10-07

import { render, screen, fireEvent, within, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
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
    expect(labels).toContain('365 days');
    expect(labels).toContain('Custom date');
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

describe('APIKeysTab custom expiry date', () => {
  // Noon local on 2026-11-20: the picker runs from 2026-11-21 to 2029-11-19
  // (1095 days; three calendar years would be 1096 across 2028's leap day).
  const NOW = new Date(2026, 10, 20, 12, 0, 0);

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(NOW);
    vi.mocked(api.listAPIKeys).mockReset();
    vi.mocked(api.createAPIKey).mockReset();
    vi.mocked(api.listAPIKeys).mockResolvedValue([]);
    vi.mocked(api.createAPIKey).mockResolvedValue({
      id: 'k3',
      name: 'Riddler',
      token: 'abk_synthetic',
      scopes: [],
      created_at: NOW.toISOString(),
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  async function openDialog() {
    render(<APIKeysTab />);
    fireEvent.click(await screen.findByRole('button', { name: /create api key/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getAllByRole('textbox')[0], { target: { value: 'Riddler' } });
    return dialog;
  }

  async function chooseCustom(dialog: HTMLElement) {
    fireEvent.mouseDown(within(dialog).getByRole('combobox'));
    fireEvent.click(await screen.findByRole('option', { name: 'Custom date' }));
    return within(dialog).getByLabelText('Expiry date') as HTMLInputElement;
  }

  it('bounds the picker from tomorrow to today plus 1095 days', async () => {
    const dialog = await openDialog();
    const input = await chooseCustom(dialog);
    expect(input).toHaveAttribute('type', 'date');
    expect(input).toHaveAttribute('min', '2026-11-21');
    expect(input).toHaveAttribute('max', '2029-11-19');
  });

  it('sends the latest date as expires_in_days 1095 and previews it first', async () => {
    const dialog = await openDialog();
    const input = await chooseCustom(dialog);
    const create = within(dialog).getByRole('button', { name: /^create$/i });
    expect(create).toBeDisabled();
    fireEvent.change(input, { target: { value: '2029-11-19' } });
    const preview = within(dialog).getByTestId('api-key-expiry-preview');
    expect(preview).toHaveTextContent(
      `Expires ${new Date(NOW.getTime() + 1095 * 24 * HOUR).toLocaleString()}`
    );
    expect(create).toBeEnabled();
    fireEvent.click(create);
    await waitFor(() => expect(api.createAPIKey).toHaveBeenCalledTimes(1));
    expect(api.createAPIKey).toHaveBeenCalledWith({
      name: 'Riddler',
      description: '',
      scopes: [],
      expires_in_days: 1095,
    });
  });

  it('sends tomorrow as one day', async () => {
    const dialog = await openDialog();
    fireEvent.change(await chooseCustom(dialog), { target: { value: '2026-11-21' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^create$/i }));
    await waitFor(() => expect(api.createAPIKey).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.createAPIKey).mock.calls[0][0].expires_in_days).toBe(1);
  });

  it('refuses a typed date past three years or not in the future', async () => {
    const dialog = await openDialog();
    const input = await chooseCustom(dialog);
    const create = within(dialog).getByRole('button', { name: /^create$/i });
    for (const value of ['2029-11-20', '2026-11-20', '2025-01-01']) {
      fireEvent.change(input, { target: { value } });
      expect(create).toBeDisabled();
      expect(within(dialog).getByText(/Pick a date from 2026-11-21 to 2029-11-19/)).toBeInTheDocument();
    }
    fireEvent.click(create);
    expect(api.createAPIKey).not.toHaveBeenCalled();
  });

  it('previews a preset and still sends it as expires_in_days', async () => {
    const dialog = await openDialog();
    expect(within(dialog).getByTestId('api-key-expiry-preview')).toHaveTextContent(
      `Expires ${new Date(NOW.getTime() + 90 * 24 * HOUR).toLocaleString()}`
    );
    fireEvent.click(within(dialog).getByRole('button', { name: /^create$/i }));
    await waitFor(() => expect(api.createAPIKey).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.createAPIKey).mock.calls[0][0]).toEqual({
      name: 'Riddler',
      description: '',
      scopes: [],
      expires_in_days: 90,
    });
  });
});
