// file: web/src/pages/Works.test.tsx
// version: 1.2.0
// guid: 6a1ce53a-f243-45d8-a45c-d3f9897179a4
// last-edited: 2026-11-05

import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { Works } from './Works';
import * as api from '../services/api';
import { renderWithProviders } from '../test/renderWithProviders';
import { useLocation } from 'react-router-dom';

vi.mock('../services/api', () => ({
  getWorks: vi.fn(),
}));

describe('Works page', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('shows a table when works exist', async () => {
    vi.mocked(api.getWorks).mockResolvedValue([
      {
        id: 'work-1',
        title: 'The Dark Knight Returns',
        author_id: 1,
        series_id: 2,
        alt_titles: ['Batman: The Dark Knight'],
      },
    ]);

    renderWithProviders(<Works />);

    const row = await screen.findByRole('row', { name: /The Dark Knight Returns/i });
    expect(within(row).getByText('work-1')).toBeVisible();
    expect(within(row).getAllByText('1')).toHaveLength(2);
    expect(within(row).getByText('2')).toBeVisible();
  });

  it('shows empty state when no works exist', async () => {
    vi.mocked(api.getWorks).mockResolvedValue([]);

    renderWithProviders(<Works />);

    expect(
      await screen.findByText(
        /No works found yet\. Works are created during scans and metadata imports\./i
      )
    ).toBeVisible();
  });

  it('shows error and retries load', async () => {
    const getWorksMock = vi
      .mocked(api.getWorks)
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce([
        {
          id: 'work-2',
          title: 'Hush',
          author_id: 3,
        },
      ]);

    renderWithProviders(<Works />);

    expect(await screen.findByText('boom')).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));

    await waitFor(() => {
      expect(getWorksMock).toHaveBeenCalledTimes(2);
    });
    expect(await screen.findByText('Hush')).toBeVisible();
  });

  describe('sort in the address bar', () => {
    const works = [
      { id: 'w1', title: 'Year One', author_id: 1 },
      { id: 'w2', title: 'The Long Halloween', author_id: 2 },
      { id: 'w3', title: 'Arkham Asylum', author_id: 3 },
    ];
    function Where() {
      const loc = useLocation();
      return <div data-testid="where">{loc.search}</div>;
    }
    const titles = () =>
      screen
        .getAllByRole('row')
        .slice(1)
        .map((r) => within(r).getAllByRole('cell')[0].textContent);

    it('opens in the order the URL names', async () => {
      vi.mocked(api.getWorks).mockResolvedValue(works as never);
      renderWithProviders(<Works />, { initialEntries: ['/works?sort=title&order=desc'] });
      await screen.findByText('Year One');
      expect(titles()).toEqual(['Year One', 'The Long Halloween', 'Arkham Asylum']);
    });

    it('writes the sort on a header click, omitting the default', async () => {
      vi.mocked(api.getWorks).mockResolvedValue(works as never);
      renderWithProviders(
        <>
          <Works />
          <Where />
        </>,
        { initialEntries: ['/works?keep=1'] }
      );
      await screen.findByText('Year One');
      fireEvent.click(screen.getByText('Title')); // already the sort: flips to desc
      await waitFor(() =>
        expect(screen.getByTestId('where').textContent).toBe('?keep=1&order=desc')
      );
      fireEvent.click(screen.getByText('Title'));
      await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('?keep=1'));
      expect(api.getWorks).toHaveBeenCalledTimes(1);
    });
  });
});
