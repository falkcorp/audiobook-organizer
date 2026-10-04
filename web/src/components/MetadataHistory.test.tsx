// file: web/src/components/MetadataHistory.test.tsx
// version: 1.1.0
// guid: e539214d-79fe-4fb5-b93b-36d9f7076346
// last-edited: 2026-10-04

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { MetadataHistory } from './MetadataHistory';
import { historyRowKey } from './metadataHistoryKey';
import * as api from '../services/api';
import type { MetadataChangeRecord } from '../services/api';

vi.mock('../services/api', async () => {
  const actual = await vi.importActual<typeof import('../services/api')>('../services/api');
  return {
    ...actual,
    getBookMetadataHistory: vi.fn(),
    getBookCOWVersions: vi.fn(),
  };
});

const row = (id: number, field: string, changedAt: string): MetadataChangeRecord => ({
  id,
  book_id: 'b1',
  field,
  previous_value: '"old"',
  new_value: '"new"',
  change_type: 'manual',
  source: 'manual',
  changed_at: changedAt,
});

describe('MetadataHistory', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('keys rows on id plus field', () => {
    expect(historyRowKey({ id: 5, field: 'title' })).not.toEqual(historyRowKey({ id: 5, field: 'series' }));
  });

  // One edit stamps every row it records with the same id. An older row of
  // a field must not borrow the undo button of another field's newest row
  // that happens to share its id, and React must not see duplicate keys.
  it('offers undo only on the newest row of each field when ids repeat', async () => {
    vi.mocked(api.getBookMetadataHistory).mockResolvedValue([
      row(9, 'series', '2026-10-03T12:00:00Z'),
      row(5, 'title', '2026-10-03T11:00:00Z'),
      row(5, 'series', '2026-10-03T11:00:00Z'),
    ]);
    vi.mocked(api.getBookCOWVersions).mockResolvedValue([]);
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});

    renderWithProviders(<MetadataHistory bookId="b1" open onClose={() => {}} />);

    await waitFor(() => expect(screen.getAllByRole('row').length).toBeGreaterThan(3));
    expect(screen.getAllByLabelText(/^Undo this /)).toHaveLength(2);
    const duplicateKey = consoleError.mock.calls.some((args) =>
      args.some((a) => typeof a === 'string' && a.includes('same key')),
    );
    expect(duplicateKey).toBe(false);
  });

  // A dropped stale series object is the store's bookkeeping: the server
  // refuses its undo (409), so no undo button is offered, and the field has a
  // readable label.
  it('offers no undo on a series-object-drop row', async () => {
    vi.mocked(api.getBookMetadataHistory).mockResolvedValue([
      {
        ...row(7, 'series_object', '2026-10-04T12:00:00Z'),
        previous_value: '"Vanished Series"',
        new_value: '""',
        change_type: 'series-object-drop',
        source: 'series_invariant',
      },
      row(5, 'title', '2026-10-03T11:00:00Z'),
    ]);
    vi.mocked(api.getBookCOWVersions).mockResolvedValue([]);

    renderWithProviders(<MetadataHistory bookId="b1" open onClose={() => {}} />);

    await waitFor(() => expect(screen.getByText('Vanished Series')).toBeInTheDocument());
    expect(screen.getByText('Stale series object')).toBeInTheDocument();
    expect(screen.getAllByLabelText(/^Undo this /)).toHaveLength(1);
    expect(screen.queryByLabelText('Undo this Stale series object change')).toBeNull();
  });
});
