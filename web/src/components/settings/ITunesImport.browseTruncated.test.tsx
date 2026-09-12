// file: web/src/components/settings/ITunesImport.browseTruncated.test.tsx
// version: 1.0.0
// guid: 3becf020-76aa-45fe-85b0-0f0080555e26
// last-edited: 2026-09-12

import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return {
    ...actual,
    getConfig: vi.fn().mockResolvedValue({}),
    getITunesBooks: vi.fn(),
    getITunesImportStatus: vi.fn().mockResolvedValue({ status: 'completed' }),
    getITunesLibraryStatus: vi.fn().mockResolvedValue(null),
    previewITunesWriteBack: vi.fn().mockResolvedValue({ items: [], total: 0 }),
    importITunesLibrary: vi.fn(),
    startITunesSync: vi.fn(),
    updateConfig: vi.fn(),
    validateITunesLibrary: vi.fn(),
    writeBackITunesLibrary: vi.fn(),
    cancelOperation: vi.fn(),
  };
});

import { getITunesBooks, type ITunesBookMapping } from '../../services/api';
import { ToastProvider } from '../toast/ToastProvider';
import { ITunesImport } from './ITunesImport';

const items: ITunesBookMapping[] = [
  {
    book_id: 'b1',
    title: 'Match One',
    author: 'Author',
    itunes_persistent_id: 'PID0001',
    ao_path: 'books/one.m4b',
    local_path: 'books/one.m4b',
  },
];

// Opens the write-back dialog through the "Force Sync to iTunes" confirm and
// switches to the "Browse & Select" tab, which triggers the browse load.
async function openBrowseTab() {
  render(
    <ToastProvider>
      <ITunesImport />
    </ToastProvider>
  );
  fireEvent.click(screen.getByRole('button', { name: /force sync to itunes/i }));
  fireEvent.click(await screen.findByRole('button', { name: /^force sync$/i }));
  fireEvent.click(await screen.findByRole('tab', { name: /browse & select/i }));
  await waitFor(() => expect(getITunesBooks).toHaveBeenCalled());
}

describe('ITunesImport browse — truncated search results', () => {
  beforeEach(() => {
    vi.mocked(getITunesBooks).mockReset();
    localStorage.clear();
  });

  it('shows the refine-search notice and a lower-bound total when truncated', async () => {
    vi.mocked(getITunesBooks).mockResolvedValue({ items, count: 125, truncated: true });
    await openBrowseTab();

    const notice = await screen.findByTestId('itunes-browse-truncated');
    expect(notice).toHaveTextContent(/showing the first 125 matches/i);
    expect(notice).toHaveTextContent(/refine the search/i);
    // Pagination is bounded by the fetched count and marks it as a lower bound.
    expect(screen.getByText('1–25 of 125+')).toBeInTheDocument();
  });

  it('shows no notice and an exact total when the response is not truncated', async () => {
    vi.mocked(getITunesBooks).mockResolvedValue({ items, count: 125 });
    await openBrowseTab();

    expect(await screen.findByText('1–25 of 125')).toBeInTheDocument();
    expect(screen.queryByTestId('itunes-browse-truncated')).not.toBeInTheDocument();
  });
});
