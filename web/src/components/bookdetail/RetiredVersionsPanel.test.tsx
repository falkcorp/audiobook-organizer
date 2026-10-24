// file: web/src/components/bookdetail/RetiredVersionsPanel.test.tsx
// version: 1.0.0
// guid: 2d6f81a4-5b3c-4e90-8a17-c4f09e7b3d52
// last-edited: 2026-10-23

import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { loginPageResponse } from '../../test/loginRedirect';
import { RetiredVersionsPanel } from './RetiredVersionsPanel';
import type { RetiredVersion } from '../../services/versionApi';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function version(over: Partial<RetiredVersion> = {}): RetiredVersion {
  return {
    id: 'v1',
    book_id: 'holder-1',
    retired_from_book_id: 'shell-1',
    retired_from_title: 'The Shell Title',
    shell_exists: true,
    retired_at: '2026-10-02T12:00:00Z',
    source_original_path: '/lib/shell',
    ingest_date: '2026-01-01T00:00:00Z',
    file_count: 2,
    total_size: 3 * 1024 * 1024,
    total_duration: 3660,
    files: [
      { id: 'f1', file_path: '/lib/shell/01.m4b', missing: false },
      { id: 'f2', file_path: '/lib/shell/02.m4b', missing: true },
    ],
    ...over,
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface Call {
  url: string;
  method: string;
}

// Stubs fetch: GET .../retired-versions answers the current `versions` list;
// POST .../restore runs `onRestore` (which may change the list).
function stubApi(opts: {
  versions: () => RetiredVersion[];
  onRestore?: () => Response;
}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = (init?.method ?? 'GET').toUpperCase();
      calls.push({ url, method });
      if (method === 'POST') return opts.onRestore ? opts.onRestore() : json({ data: {} });
      return json({ data: { versions: opts.versions(), count: opts.versions().length } });
    })
  );
  return calls;
}

async function expand() {
  fireEvent.click(await screen.findByRole('button', { name: /Retired versions/ }));
}

describe('RetiredVersionsPanel', () => {
  it('is collapsed with a count and lists a row per retired version when opened', async () => {
    const calls = stubApi({ versions: () => [version(), version({ id: 'v2', file_count: 1 })] });
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" />);

    expect(await screen.findByText('Retired versions (2)')).toBeInTheDocument();
    expect(calls[0]).toEqual({ url: '/api/v1/audiobooks/holder-1/retired-versions', method: 'GET' });

    await expand();
    const row = screen.getByTestId('retired-version-v1');
    expect(within(row).getByRole('link', { name: 'The Shell Title' })).toHaveAttribute(
      'href',
      '/library/shell-1'
    );
    expect(within(row).getByText('3.0 MB')).toBeInTheDocument();
    expect(within(row).getByText('1h 1m')).toBeInTheDocument();
    expect(screen.getByTestId('retired-version-v2')).toBeInTheDocument();
  });

  it('shows the empty state when the book holds no retired versions', async () => {
    stubApi({ versions: () => [] });
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" />);
    expect(await screen.findByText('Retired versions (0)')).toBeInTheDocument();
    await expand();
    expect(screen.getByText('No retired versions.')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows an error, not the empty state, when the session has expired', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" />);
    await expand();
    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
    expect(screen.queryByText('No retired versions.')).not.toBeInTheDocument();
    expect(screen.queryByText(/Retired versions \(/)).not.toBeInTheDocument();
  });

  it('lists the files of a version on demand and flags a missing one', async () => {
    stubApi({ versions: () => [version()] });
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" />);
    await expand();
    fireEvent.click(await screen.findByRole('button', { name: 'Show files' }));
    const list = screen.getByTestId('retired-files-v1');
    expect(within(list).getByText('/lib/shell/01.m4b')).toBeInTheDocument();
    expect(within(list).getByText(/\/lib\/shell\/02\.m4b \(missing\)/)).toBeInTheDocument();
  });

  it('restore POSTs to the restore route, refetches the list and calls onRestored', async () => {
    let restored = false;
    const calls = stubApi({
      versions: () => (restored ? [] : [version()]),
      onRestore: () => {
        restored = true;
        return json({ data: { version_id: 'v1', restored_to_book: 'shell-1', restored_files: 2 } });
      },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const onRestored = vi.fn();
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" onRestored={onRestored} />);
    await expand();

    fireEvent.click(await screen.findByRole('button', { name: 'Restore' }));

    await waitFor(() => expect(onRestored).toHaveBeenCalledTimes(1));
    expect(
      calls.filter((c) => c.method === 'POST').map((c) => c.url)
    ).toEqual(['/api/v1/audiobooks/holder-1/retired-versions/v1/restore']);
    // The list was fetched again after the POST, and the restored row is gone.
    const postIndex = calls.findIndex((c) => c.method === 'POST');
    expect(calls.slice(postIndex + 1).some((c) => c.method === 'GET')).toBe(true);
    expect(await screen.findByText('Retired versions (0)')).toBeInTheDocument();
    expect(screen.getByText(/Restored 2 files to "The Shell Title"/)).toBeInTheDocument();
    expect(screen.queryByTestId('retired-version-v1')).not.toBeInTheDocument();
  });

  it('does nothing when the confirmation is declined', async () => {
    const calls = stubApi({ versions: () => [version()] });
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    const onRestored = vi.fn();
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" onRestored={onRestored} />);
    await expand();
    fireEvent.click(await screen.findByRole('button', { name: 'Restore' }));
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    expect(onRestored).not.toHaveBeenCalled();
  });

  it('shows the server reason when a restore is refused and keeps the row', async () => {
    stubApi({
      versions: () => [version()],
      onRestore: () => json({ error: 'the book this version was retired from is not in the trash' }, 409),
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const onRestored = vi.fn();
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" onRestored={onRestored} />);
    await expand();
    fireEvent.click(await screen.findByRole('button', { name: 'Restore' }));
    expect(await screen.findByText(/is not in the trash/)).toBeInTheDocument();
    expect(onRestored).not.toHaveBeenCalled();
    expect(screen.getByTestId('retired-version-v1')).toBeInTheDocument();
  });

  it('disables restore for a version whose book was purged', async () => {
    stubApi({ versions: () => [version({ shell_exists: false, retired_from_title: undefined })] });
    renderWithProviders(<RetiredVersionsPanel bookId="holder-1" />);
    await expand();
    expect(await screen.findByRole('button', { name: 'Restore' })).toBeDisabled();
    expect(screen.getByText('purged')).toBeInTheDocument();
  });

  it('reloads when refreshKey changes', async () => {
    const calls = stubApi({ versions: () => [version()] });
    const { rerender } = renderWithProviders(<RetiredVersionsPanel bookId="holder-1" refreshKey={0} />);
    await screen.findByText('Retired versions (1)');
    rerender(<RetiredVersionsPanel bookId="holder-1" refreshKey={1} />);
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2));
  });
});
