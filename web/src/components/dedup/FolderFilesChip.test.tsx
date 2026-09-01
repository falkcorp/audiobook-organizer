// file: web/src/components/dedup/FolderFilesChip.test.tsx
// version: 1.0.0
// guid: 7c3e1f84-9a02-4d16-b5e7-2f8d61a4c093
// last-edited: 2026-09-01
//
// Written alongside the perf change that stopped this component building its
// <Popover> on every render. The component had NO tests at all, and the change
// is invisible to every other suite: if the gate were wrong the popover would
// simply never open, and the dupes lane would still render, still pass, and
// still be measurably FASTER -- a silent regression that looks like a win.
//
// NOT COVERED HERE, deliberately: the gate is "has ever been opened", not "is
// open now", so that closing does not rip MUI's Modal out of the tree mid-exit
// and truncate its fade. That distinction is transition TIMING -- jsdom fires
// no transitionend, and the node is gone by the first waitFor poll under either
// gate, so a test here would assert nothing and pass against both. It is worth
// getting right anyway because theme.ts zeroes the exit transition for MuiMenu
// ONLY and explicitly leaves MuiPopover animating (see theme.ts:307, and the
// stuck-modal incident it records).

import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { FolderFilesChip } from './FolderFilesChip';
import { appTheme } from '../../theme';
import * as api from '../../services/api';

vi.mock('../../services/api');

beforeEach(() => {
  // This project sets neither `clearMocks` nor `restoreMocks` in its vitest
  // config, so mock.calls accumulate across tests in a file. The "does not
  // refetch" assertion below counts calls, and without this clear it reads
  // the previous test's click too and fails for the wrong reason.
  vi.clearAllMocks();
  vi.mocked(api.getBookFiles).mockResolvedValue({
    files: [{ id: 'f1', file_path: '/library/books/A/T/x.m4b', format: 'm4b' }],
  } as unknown as Awaited<ReturnType<typeof api.getBookFiles>>);
});

function renderChip() {
  return render(
    <ThemeProvider theme={appTheme}>
      <FolderFilesChip bookId="b1" />
    </ThemeProvider>
  );
}

describe('FolderFilesChip', () => {
  it('builds no popover, and fetches nothing, until the chip is clicked', () => {
    renderChip();

    expect(screen.getByText('Files')).toBeInTheDocument();
    expect(screen.queryByRole('presentation')).toBeNull();
    expect(api.getBookFiles).not.toHaveBeenCalled();
  });

  it('opens on click and lazily fetches the file list', async () => {
    renderChip();
    await userEvent.click(screen.getByText('Files'));

    expect(await screen.findByText('x.m4b')).toBeInTheDocument();
    expect(api.getBookFiles).toHaveBeenCalledWith('b1', expect.anything());
  });

  it('does not refetch when reopened', async () => {
    // The component's own contract is "lazy-load once". A gate that discarded
    // the popover's loaded state on close would turn one fetch per book into
    // one per click -- and would still look correct on screen.
    renderChip();
    await userEvent.click(screen.getByText('Files'));
    await screen.findByText('x.m4b');

    await userEvent.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByText('x.m4b')).toBeNull());

    await userEvent.click(screen.getByText('1 Files'));
    expect(await screen.findByText('x.m4b')).toBeInTheDocument();
    expect(api.getBookFiles).toHaveBeenCalledTimes(1);
  });
});
