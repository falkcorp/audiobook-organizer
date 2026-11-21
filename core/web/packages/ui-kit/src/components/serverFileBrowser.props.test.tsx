// file: core/web/packages/ui-kit/src/components/serverFileBrowser.props.test.tsx
// version: 0.1.0
// guid: 8a059355-07a4-4f0f-8d54-62940f8c60a9
// last-edited: 2026-11-21

import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ServerFileBrowser, type BrowseResult } from './ServerFileBrowser';

const LISTING: BrowseResult = {
  path: '/wayne',
  count: 3,
  items: [
    { name: 'cave', path: '/wayne/cave', is_dir: true, excluded: false },
    { name: 'attic', path: '/wayne/attic', is_dir: true, excluded: true },
    { name: 'ledger.txt', path: '/wayne/ledger.txt', is_dir: false, size: 2048 },
  ],
  disk_info: { exists: true, readable: true, writable: false, total_bytes: 2048, free_bytes: 1024 },
};

function browseOf(result: BrowseResult = LISTING) {
  return vi.fn(async (path: string) => ({ ...result, path: result.path || path }));
}

async function ready() {
  await screen.findByText('cave');
}

const rightClick = (name: string) => fireEvent.contextMenu(screen.getByText(name));

describe('ServerFileBrowser props', () => {
  it('lists what browse returns, folders first, and asks for the initial path', async () => {
    const browse = browseOf();
    render(<ServerFileBrowser browse={browse} initialPath="/wayne" />);
    await ready();
    expect(browse).toHaveBeenCalledWith('/wayne');
    const names = screen.getAllByRole('button').map((b) => b.textContent ?? '');
    expect(names.findIndex((t) => t.startsWith('cave'))).toBeLessThan(names.findIndex((t) => t.startsWith('ledger.txt')));
    expect(screen.getByText('Readable')).toBeInTheDocument();
  });

  it('shows no context menu with browse alone', async () => {
    render(<ServerFileBrowser browse={browseOf()} />);
    await ready();
    rightClick('cave');
    rightClick('attic');
    expect(screen.queryByText('Exclude from scan')).not.toBeInTheDocument();
    expect(screen.queryByText('Include in scan')).not.toBeInTheDocument();
  });

  it('with include and exclude, offers Exclude on a live folder and calls exclude, then reloads', async () => {
    const browse = browseOf();
    const exclude = vi.fn(async () => ({}));
    const include = vi.fn(async () => {});
    render(<ServerFileBrowser browse={browse} include={include} exclude={exclude} initialPath="/wayne" />);
    await ready();
    rightClick('cave');
    fireEvent.click(await screen.findByText('Exclude from scan'));
    await waitFor(() => expect(exclude).toHaveBeenCalledWith('/wayne/cave'));
    expect(include).not.toHaveBeenCalled();
    await screen.findByText('Folder excluded from scan.');
    expect(browse).toHaveBeenCalledTimes(2);
  });

  it('offers Include on an excluded folder and calls include', async () => {
    const exclude = vi.fn(async () => ({}));
    const include = vi.fn(async () => {});
    render(<ServerFileBrowser browse={browseOf()} include={include} exclude={exclude} initialPath="/wayne" />);
    await ready();
    rightClick('attic');
    fireEvent.click(await screen.findByText('Include in scan'));
    await waitFor(() => expect(include).toHaveBeenCalledWith('/wayne/attic'));
    expect(exclude).not.toHaveBeenCalled();
    await screen.findByText('Folder included in scan.');
  });

  it('offers only the action the host supplied: exclude alone does not open a menu on an excluded folder', async () => {
    render(<ServerFileBrowser browse={browseOf()} exclude={vi.fn(async () => ({}))} />);
    await ready();
    rightClick('attic');
    expect(screen.queryByText('Include in scan')).not.toBeInTheDocument();
    rightClick('cave');
    expect(await screen.findByText('Exclude from scan')).toBeInTheDocument();
  });

  it('never opens a menu on a file', async () => {
    render(<ServerFileBrowser browse={browseOf()} include={vi.fn()} exclude={vi.fn()} />);
    await ready();
    rightClick('ledger.txt');
    expect(screen.queryByText('Exclude from scan')).not.toBeInTheDocument();
  });

  it('shows the failure and keeps going when exclude rejects', async () => {
    const exclude = vi.fn(async () => {
      throw new Error('Arkham says no');
    });
    render(<ServerFileBrowser browse={browseOf()} include={vi.fn()} exclude={exclude} />);
    await ready();
    rightClick('cave');
    fireEvent.click(await screen.findByText('Exclude from scan'));
    expect((await screen.findAllByText('Arkham says no')).length).toBeGreaterThan(0);
  });

  it('shows the error when browse rejects', async () => {
    const browse = vi.fn(async () => {
      throw new Error('vault sealed');
    });
    const log = vi.spyOn(console, 'error').mockImplementation(() => {});
    render(<ServerFileBrowser browse={browse} />);
    expect(await screen.findByText('vault sealed')).toBeInTheDocument();
    log.mockRestore();
  });

  it('reports the listed folder through onSelect once, and does not reload when the parent re-renders with a new onSelect or browse', async () => {
    const browse = browseOf();
    const first = vi.fn();
    const { rerender } = render(<ServerFileBrowser browse={browse} onSelect={first} initialPath="/wayne" />);
    await ready();
    expect(first).toHaveBeenCalledWith('/wayne', true);
    const second = vi.fn();
    rerender(<ServerFileBrowser browse={browse} onSelect={second} initialPath="/wayne" />);
    const inlineBrowse = browseOf();
    rerender(<ServerFileBrowser browse={inlineBrowse} onSelect={vi.fn()} initialPath="/wayne" />);
    await new Promise((r) => setTimeout(r, 20));
    expect(browse).toHaveBeenCalledTimes(1);
    expect(inlineBrowse).not.toHaveBeenCalled();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).not.toHaveBeenCalled();
  });

  it('enters a folder on click, browsing its path', async () => {
    const browse = vi.fn(async (path: string) => (path === '/wayne' ? LISTING : { path, items: [] }));
    render(<ServerFileBrowser browse={browse} initialPath="/wayne" />);
    await ready();
    fireEvent.click(screen.getByText('cave'));
    await waitFor(() => expect(browse).toHaveBeenLastCalledWith('/wayne/cave'));
  });
});
