// file: web/src/components/ChangeLog.test.tsx
// version: 1.1.0
// guid: 6e2f1a4c-9b3d-4e7a-8c1f-5d2b6a9e0f3c
// last-edited: 2026-08-23

import { describe, it, expect, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { ChangeLog } from './ChangeLog';
import { fetchActivity } from '../services/activityApi';
import type { ActivityEntry } from '../services/activityApi';

vi.mock('../services/activityApi', async () => {
  const actual =
    await vi.importActual<typeof import('../services/activityApi')>('../services/activityApi');
  return {
    ...actual,
    fetchActivity: vi.fn(),
  };
});

describe('ChangeLog', () => {
  const tagWriteEntry: ActivityEntry = {
    id: 'a1',
    timestamp: '2026-08-23T10:00:00Z',
    tier: 'change',
    type: 'tag_write',
    level: 'info',
    source: 'test',
    summary: 'Wrote tags',
  };

  const importEntry: ActivityEntry = {
    id: 'a2',
    timestamp: '2026-08-23T09:00:00Z',
    tier: 'change',
    type: 'import',
    level: 'info',
    source: 'test',
    summary: 'Imported book',
  };

  it('gives an actionable entry a real, keyboard-reachable "Compare snapshot" button', async () => {
    vi.mocked(fetchActivity).mockResolvedValue({
      entries: [tagWriteEntry],
      total: 1,
    });
    const onCompareSnapshot = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(<ChangeLog bookId="book1" onCompareSnapshot={onCompareSnapshot} />);

    // Tab-reachable: this is the only focusable element on the page, so a
    // single Tab from body must land on it. A tabIndex of -1 (or no
    // tabIndex at all on a non-button element) would fail this.
    await user.tab();
    const compareButton = screen.getByRole('button', { name: /compare snapshot/i });
    expect(compareButton).toHaveFocus();

    await user.keyboard('{Enter}');
    expect(onCompareSnapshot).toHaveBeenCalledTimes(1);
    expect(onCompareSnapshot).toHaveBeenCalledWith(tagWriteEntry.timestamp);

    await user.keyboard(' ');
    expect(onCompareSnapshot).toHaveBeenCalledTimes(2);

    // The row's own content must still be announced normally -- it must NOT
    // be swallowed by an aria-label on an ancestor role="button".
    expect(screen.getByText('Wrote tags')).toBeInTheDocument();
    expect(screen.getByText('Tag Write')).toBeInTheDocument();
  });

  it('renders no "Compare snapshot" button for a non-actionable entry', async () => {
    vi.mocked(fetchActivity).mockResolvedValue({
      entries: [importEntry],
      total: 1,
    });

    renderWithProviders(<ChangeLog bookId="book1" onCompareSnapshot={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText('Imported book')).toBeInTheDocument();
    });

    expect(screen.queryByRole('button', { name: /compare snapshot/i })).not.toBeInTheDocument();
  });

  it('pressing Enter on the Revert button does not double-fire onCompareSnapshot', async () => {
    // idx > 0 is required for the Revert button to render, so pad with a
    // leading non-revertable entry ahead of the actionable one under test.
    vi.mocked(fetchActivity).mockResolvedValue({
      entries: [importEntry, tagWriteEntry],
      total: 2,
    });
    const onCompareSnapshot = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(<ChangeLog bookId="book1" onCompareSnapshot={onCompareSnapshot} />);

    const revertButton = await screen.findByRole('button', { name: /revert/i });
    revertButton.focus();
    expect(revertButton).toHaveFocus();

    await user.keyboard('{Enter}');

    expect(onCompareSnapshot).not.toHaveBeenCalled();

    // Anti-over-suppression: the Compare button's own Enter handling still
    // fires exactly once for a known-good input once the guard above is in
    // place (i.e. this isn't a global keydown suppressor gone too broad).
    const compareButton = screen.getByRole('button', { name: /compare snapshot/i });
    compareButton.focus();
    await user.keyboard('{Enter}');
    expect(onCompareSnapshot).toHaveBeenCalledTimes(1);
    expect(onCompareSnapshot).toHaveBeenCalledWith(tagWriteEntry.timestamp);
  });

  it('does not double-fire onCompareSnapshot when the Compare button is clicked with a mouse', async () => {
    vi.mocked(fetchActivity).mockResolvedValue({
      entries: [tagWriteEntry],
      total: 1,
    });
    const onCompareSnapshot = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(<ChangeLog bookId="book1" onCompareSnapshot={onCompareSnapshot} />);

    const compareButton = await screen.findByRole('button', { name: /compare snapshot/i });
    await user.click(compareButton);

    // The button's click bubbles up to the row's own onClick (also wired to
    // onCompareSnapshot) unless the button's handler stops propagation.
    expect(onCompareSnapshot).toHaveBeenCalledTimes(1);
  });
});
