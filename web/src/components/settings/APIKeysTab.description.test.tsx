// file: web/src/components/settings/APIKeysTab.description.test.tsx
// version: 0.1.0
// guid: ed8673df-ac13-4246-a059-ed7d04d2307c
// last-edited: 2026-11-22

// The create dialog's one-line description box and the tooltip that shows it on the key's name.

import { render, screen, fireEvent, within, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { API_KEY_DESCRIPTION_MAX_CHARS } from '../../utils/apiKeyDescription';
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
    name: 'bat-signal',
    scopes: ['books:view'],
    status: 'active',
    created_at: new Date(Date.now() - HOUR).toISOString(),
    use_count: 0,
    identifier: 'k1',
    days_since_last_use: null,
    never_used: true,
    ...overrides,
  };
}

async function openDialog() {
  render(<APIKeysTab />);
  fireEvent.click(await screen.findByRole('button', { name: /create api key/i }));
  return screen.findByRole('dialog');
}

describe('APIKeysTab description', () => {
  beforeEach(() => {
    vi.mocked(api.listAPIKeys).mockReset();
    vi.mocked(api.createAPIKey).mockReset();
    vi.mocked(api.listAPIKeys).mockResolvedValue([]);
    vi.mocked(api.createAPIKey).mockResolvedValue({
      id: 'k2',
      name: 'bat-signal',
      token: 'abk_synthetic',
      scopes: [],
      created_at: new Date().toISOString(),
    });
  });

  it('sends the trimmed description with the create', async () => {
    const dialog = await openDialog();
    fireEvent.change(within(dialog).getByLabelText(/^name/i), { target: { value: 'bat-signal' } });
    fireEvent.change(within(dialog).getByLabelText('Description'), { target: { value: '  Alfred phone  ' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^create$/i }));
    await waitFor(() => expect(api.createAPIKey).toHaveBeenCalled());
    expect(vi.mocked(api.createAPIKey).mock.calls[0][0]).toMatchObject({
      name: 'bat-signal',
      description: 'Alfred phone',
    });
  });

  it('sends no description when the box is blank', async () => {
    const dialog = await openDialog();
    fireEvent.change(within(dialog).getByLabelText(/^name/i), { target: { value: 'bat-signal' } });
    fireEvent.change(within(dialog).getByLabelText('Description'), { target: { value: '   ' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /^create$/i }));
    await waitFor(() => expect(api.createAPIKey).toHaveBeenCalled());
    expect(vi.mocked(api.createAPIKey).mock.calls[0][0]).not.toHaveProperty('description');
  });

  it('is a single line capped at core\'s limit', async () => {
    const dialog = await openDialog();
    const box = within(dialog).getByLabelText('Description');
    expect(box.tagName).toBe('INPUT');
    expect(box).toHaveAttribute('maxlength', String(API_KEY_DESCRIPTION_MAX_CHARS));
    expect(API_KEY_DESCRIPTION_MAX_CHARS).toBe(1024);
  });

  it('shows the description as a tooltip on the key name', async () => {
    vi.mocked(api.listAPIKeys).mockResolvedValue([key({ description: 'Spare for the Batcave' })]);
    render(<APIKeysTab />);
    const name = await screen.findByText('bat-signal');
    fireEvent.mouseOver(name);
    expect(await screen.findByRole('tooltip')).toHaveTextContent('Spare for the Batcave');
  });

  it('shows no tooltip for a key without a description', async () => {
    vi.mocked(api.listAPIKeys).mockResolvedValue([key({})]);
    render(<APIKeysTab />);
    const name = await screen.findByText('bat-signal');
    fireEvent.mouseOver(name);
    // Past MUI's default 100 ms enter delay, so a tooltip that was going to open has opened.
    await new Promise((r) => setTimeout(r, 400));
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
    expect(name.getAttribute('aria-label') ?? '').toBe('');
  });
});
