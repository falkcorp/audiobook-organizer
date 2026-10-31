// file: web/src/components/system/ThrottlePanel.test.tsx
// version: 1.0.0
// guid: 0b6e9a52-84d1-4f3c-b7a9-2c5e18d6f340
// last-edited: 2026-10-30

import { render, screen, waitFor, fireEvent, within } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ThrottlePanel } from './ThrottlePanel';
import * as api from '../../services/api';
import * as perm from '../../hooks/useLibraryPermission';

vi.mock('../../services/api', () => ({
  getProviderThrottles: vi.fn(),
  startThrottleReset: vi.fn(),
  pollOperationV2: vi.fn(),
}));

const holds: api.ProviderThrottle[] = [
  {
    provider_id: 'audible',
    reason: 'quota',
    detail: 'daily budget spent',
    until: '2026-10-30T20:00:00Z',
    seconds_remaining: 7260,
  },
  {
    provider_id: 'google-books',
    reason: 'rate_limit',
    until: '2026-10-30T19:00:00Z',
    seconds_remaining: 90,
  },
];

describe('ThrottlePanel', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.mocked(api.getProviderThrottles).mockReset().mockResolvedValue(holds);
    vi.mocked(api.startThrottleReset).mockReset().mockResolvedValue({ operation_id: 'op-1' });
    vi.mocked(api.pollOperationV2)
      .mockReset()
      .mockResolvedValue({ id: 'op-1', status: 'completed' } as Awaited<
        ReturnType<typeof api.pollOperationV2>
      >);
  });

  it('renders each hold with its reason, detail and time left', async () => {
    render(<ThrottlePanel />);
    const row = await screen.findByTestId('throttle-audible');
    expect(row).toHaveTextContent('quota');
    expect(row).toHaveTextContent('daily budget spent');
    expect(row).toHaveTextContent('2h 1m left');
    expect(screen.getByTestId('throttle-google-books')).toHaveTextContent('1m left');
  });

  it('shows an empty state and disables Reset all when nothing is held', async () => {
    vi.mocked(api.getProviderThrottles).mockResolvedValue([]);
    render(<ThrottlePanel />);
    expect(await screen.findByText('No providers are on hold.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reset all' })).toBeDisabled();
  });

  it('Reset now starts the op for that provider for that provider, then refetches', async () => {
    render(<ThrottlePanel />);
    const row = await screen.findByTestId('throttle-audible');
    fireEvent.click(row.querySelector('button') as HTMLButtonElement);
    await waitFor(() => expect(api.startThrottleReset).toHaveBeenCalledWith('audible'));
    await waitFor(() => expect(api.getProviderThrottles).toHaveBeenCalledTimes(2));
  });

  it('Reset all starts the op with no provider', async () => {
    render(<ThrottlePanel />);
    await screen.findByTestId('throttle-audible');
    fireEvent.click(screen.getByRole('button', { name: 'Reset all' }));
    await waitFor(() => expect(api.startThrottleReset).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.startThrottleReset).mock.calls[0][0]).toBeUndefined();
  });

  it('disables every reset button while the op is running', async () => {
    let finish: (v: Awaited<ReturnType<typeof api.pollOperationV2>>) => void = () => {};
    vi.mocked(api.pollOperationV2).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      })
    );
    render(<ThrottlePanel />);
    const row = await screen.findByTestId('throttle-audible');
    fireEvent.click(row.querySelector('button') as HTMLButtonElement);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Resetting…' })).toBeDisabled());
    expect(screen.getByRole('button', { name: 'Reset all' })).toBeDisabled();
    expect(within(screen.getByTestId('throttle-google-books')).getByRole('button')).toBeDisabled();
    finish({ id: 'op-1', status: 'completed' } as Awaited<ReturnType<typeof api.pollOperationV2>>);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Reset all' })).toBeEnabled());
  });

  it('shows the error when the op fails to start', async () => {
    vi.mocked(api.startThrottleReset).mockRejectedValue(new Error('provider has no active hold'));
    render(<ThrottlePanel />);
    await screen.findByTestId('throttle-audible');
    fireEvent.click(screen.getByRole('button', { name: 'Reset all' }));
    expect(await screen.findByText('provider has no active hold')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reset all' })).toBeEnabled();
  });

  it('shows the op error when the run ends failed', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({
      id: 'op-1',
      status: 'failed',
      error_message: 'store unavailable',
    } as Awaited<ReturnType<typeof api.pollOperationV2>>);
    render(<ThrottlePanel />);
    await screen.findByTestId('throttle-audible');
    fireEvent.click(screen.getByRole('button', { name: 'Reset all' }));
    expect(await screen.findByText('store unavailable')).toBeInTheDocument();
  });

  it('shows the load error', async () => {
    vi.mocked(api.getProviderThrottles).mockRejectedValue(new Error('boom'));
    render(<ThrottlePanel />);
    expect(await screen.findByText('boom')).toBeInTheDocument();
  });

  it('disables reset buttons for a caller without scan.trigger', async () => {
    vi.spyOn(perm, 'useHasPermission').mockReturnValue(false);
    render(<ThrottlePanel />);
    const row = await screen.findByTestId('throttle-audible');
    expect(row.querySelector('button')).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Reset all' })).toBeDisabled();
  });
});
