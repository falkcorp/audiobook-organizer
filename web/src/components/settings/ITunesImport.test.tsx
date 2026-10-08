// file: web/src/components/settings/ITunesImport.test.tsx
// version: 1.0.0
// guid: 4e2c0de9-8e92-4e17-a8ce-298797579937
// last-edited: 2026-10-08

import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ITunesImport } from './ITunesImport';
import * as api from '../../services/api';

vi.mock('../../services/api', async () => {
  const actual = await vi.importActual<typeof api>('../../services/api');
  return {
    ...actual,
    getConfig: vi.fn(),
    getITunesLibraryStatus: vi.fn(),
    validateITunesLibrary: vi.fn(),
    getITunesLinkedBookCount: vi.fn(),
    importITunesLibrary: vi.fn(),
    getITunesImportStatus: vi.fn(),
  };
});

vi.mock('../../stores/useOperationsStore', () => ({
  useOperationsStore: {
    getState: () => ({ activeOperations: [], startPolling: vi.fn() }),
  },
}));

const WARNING =
  "You've imported from iTunes before. We match each album to your existing books by " +
  'iTunes ID, then by file path. Albums iTunes has re-created with new IDs, or whose files ' +
  "moved, can't be matched and will be added as new books, so you may see duplicates. " +
  'Nothing in iTunes is changed, and no files are moved.';

async function renderValidated() {
  render(<ITunesImport />);
  fireEvent.change(screen.getByLabelText('iTunes Library Path'), {
    target: { value: '/synthetic/iTunes Library.xml' },
  });
  fireEvent.click(screen.getByRole('button', { name: 'Validate Import' }));
  await screen.findByText('Validation Results');
}

describe('ITunesImport', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    vi.mocked(api.getConfig).mockResolvedValue({} as api.Config);
    vi.mocked(api.getITunesLibraryStatus).mockResolvedValue({
      changed_since_import: false,
      fingerprint_stored: '',
      last_imported: '',
      last_external_change: '',
    });
    vi.mocked(api.validateITunesLibrary).mockResolvedValue({
      total_tracks: 4,
      audiobook_tracks: 4,
      audiobook_count: 2,
      files_found: 4,
      files_missing: 0,
      duplicate_count: 0,
      estimated_import_time: '1 second',
    });
    vi.mocked(api.importITunesLibrary).mockResolvedValue({
      operation_id: 'op-synthetic',
      status: 'queued',
      message: 'queued',
    });
    vi.mocked(api.getITunesImportStatus).mockResolvedValue({
      operation_id: 'op-synthetic',
      status: 'completed',
      progress: 100,
      message: 'done',
      imported: 1,
      linked: 2,
      skipped: 3,
      failed: 0,
    });
  });

  it('has no sync controls, only the import action', async () => {
    await renderValidated();
    expect(screen.getByRole('button', { name: 'Import iTunes library' })).toBeInTheDocument();
    expect(screen.queryByText('Force Sync Options')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Sync Now' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry Failed Sync' })).not.toBeInTheDocument();
  });

  it('imports straight away when no book is linked to iTunes yet', async () => {
    vi.mocked(api.getITunesLinkedBookCount).mockResolvedValue(0);
    await renderValidated();
    fireEvent.click(screen.getByRole('button', { name: 'Import iTunes library' }));

    await waitFor(() => expect(api.importITunesLibrary).toHaveBeenCalledTimes(1));
    expect(screen.queryByText(WARNING)).not.toBeInTheDocument();
    expect(await screen.findByTestId('itunes-import-result')).toHaveTextContent(
      'Linked 2, added 1, skipped 3'
    );
  });

  it('warns before importing again; Cancel starts nothing', async () => {
    vi.mocked(api.getITunesLinkedBookCount).mockResolvedValue(5);
    await renderValidated();
    fireEvent.click(screen.getByRole('button', { name: 'Import iTunes library' }));

    const dialog = await screen.findByRole('dialog', { name: 'Import iTunes library again?' });
    expect(within(dialog).getByText(WARNING)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Import iTunes library again?' })
      ).not.toBeInTheDocument()
    );
    expect(api.importITunesLibrary).not.toHaveBeenCalled();
  });

  it('Import anyway runs the import', async () => {
    vi.mocked(api.getITunesLinkedBookCount).mockResolvedValue(5);
    await renderValidated();
    fireEvent.click(screen.getByRole('button', { name: 'Import iTunes library' }));

    const dialog = await screen.findByRole('dialog', { name: 'Import iTunes library again?' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Import anyway' }));

    await waitFor(() => expect(api.importITunesLibrary).toHaveBeenCalledTimes(1));
  });

  it('still warns when the linked-book count cannot be read', async () => {
    vi.mocked(api.getITunesLinkedBookCount).mockRejectedValue(new Error('network'));
    await renderValidated();
    fireEvent.click(screen.getByRole('button', { name: 'Import iTunes library' }));

    expect(
      await screen.findByRole('dialog', { name: 'Import iTunes library again?' })
    ).toBeInTheDocument();
    expect(api.importITunesLibrary).not.toHaveBeenCalled();
  });
});
