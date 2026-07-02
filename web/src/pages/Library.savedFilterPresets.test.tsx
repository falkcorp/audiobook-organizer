// file: web/src/pages/Library.savedFilterPresets.test.tsx
// version: 1.0.0
// guid: e7f8a9b0-c1d2-4e3f-8a9b-0c1d2e3f4a5b
// last-edited: 2026-07-01

import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, it, expect, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { Library } from './Library';
import * as api from '../services/api';
import type { SavedFilterPreset } from '../services/api';

vi.mock('../services/api', () => {
  class ApiError extends Error {
    status: number;
    data?: unknown;
    constructor(message: string, status: number, data?: unknown) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.data = data;
    }
  }
  const existingPreset: SavedFilterPreset = {
    id: 'preset-1',
    name: 'My Preset',
    filters: { author: 'Jane Doe' },
    selectedTags: [],
  };
  return {
    ApiError,
    getOperationLogsTail: vi.fn().mockResolvedValue([]),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getActiveOperations: vi.fn().mockResolvedValue([]),
    getBooks: vi.fn().mockResolvedValue({
      items: [
        {
          id: 'id-1',
          title: 'Test Book',
          file_path: '/tmp/book.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Author',
        },
      ],
      count: 1,
    }),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(1),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { path: '/tmp', book_count: 1, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    }),
    getHomeDirectory: vi.fn().mockResolvedValue('/tmp'),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getUserColumnConfig: vi.fn().mockResolvedValue(null),
    saveUserColumnConfig: vi.fn().mockResolvedValue(undefined),
    listAllUserTags: vi.fn().mockResolvedValue([]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([existingPreset]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

describe('Library saved filter presets', () => {
  it('applying a saved preset updates the active filters', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Library />
      </MemoryRouter>
    );

    const presetsButton = await screen.findByRole('button', { name: /presets/i });
    await user.click(presetsButton);

    const presetItem = await screen.findByText('My Preset');
    await user.click(presetItem);

    await waitFor(() => {
      const filtersButton = screen.getByRole('button', { name: /filters/i });
      expect(filtersButton).toHaveTextContent('1');
    });
  });

  it('saving the current filters as a new preset persists it', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Library />
      </MemoryRouter>
    );

    const presetsButton = await screen.findByRole('button', { name: /presets/i });
    await user.click(presetsButton);

    const saveMenuItem = await screen.findByText(/save current filters as preset/i);
    await user.click(saveMenuItem);

    const nameField = await screen.findByLabelText(/preset name/i);
    await user.type(nameField, 'New Preset');

    const dialog = await screen.findByRole('dialog');
    const saveButton = within(dialog).getByRole('button', { name: 'Save' });
    await user.click(saveButton);

    const saveMock = vi.mocked(api.saveSavedFilterPresets);
    await waitFor(() => {
      expect(saveMock).toHaveBeenCalled();
    });
    const savedArg = saveMock.mock.calls[saveMock.mock.calls.length - 1][0];
    expect(savedArg.some((p) => p.name === 'New Preset')).toBe(true);
    expect(savedArg.some((p) => p.name === 'My Preset')).toBe(true);
  });

  it('deleting a preset removes it from the list', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Library />
      </MemoryRouter>
    );

    const presetsButton = await screen.findByRole('button', { name: /presets/i });
    await user.click(presetsButton);

    await screen.findByText('My Preset');
    const deleteButton = await screen.findByRole('button', { name: /delete my preset/i });
    await user.click(deleteButton);

    const saveMock = vi.mocked(api.saveSavedFilterPresets);
    await waitFor(() => {
      expect(saveMock).toHaveBeenCalledWith([]);
    });

    await user.click(await screen.findByRole('button', { name: /presets/i }));
    expect(screen.queryByText('My Preset')).not.toBeInTheDocument();
  });
});
