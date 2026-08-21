// file: web/src/components/review/spine/RegroupSpine.test.tsx
// version: 1.0.0
// guid: 3f7c9e21-5a4b-4d8e-9c1f-2b6a7d0e4c53
// last-edited: 2026-08-21

/**
 * Task 6 fix round: RegroupSpine wired PathLinks into MemberRow (commit
 * a21ebdb6) with no covering test, so the wiring could silently render on one
 * lane and not another. This file exercises MemberRow directly rather than
 * the whole RegroupSpine tree -- the only thing Task 6 touched is whether
 * `entry.filePath` / `pathAliases` reach PathLinks and whether the guard
 * holds, and PathLinks.test.tsx already covers PathLinks's own
 * rendering/aliasing logic in depth.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { MemberRow } from './RegroupSpine';
import { appTheme } from '../../../theme';
import * as api from '../../../services/api';
import type { PathAlias, Config } from '../../../services/api';
import type { MemberEntry } from '../../../lib/reviewPayload';

// MemberRow receives `pathAliases` as a plain prop -- it does not call
// usePathAliases() itself (only MemberFilesDetail does, one level up). So the
// only reason PathLinks needs the api module mocked at all is that it calls
// usePathVars() (formatPath.ts) internally for display abbreviation; without
// this, getConfig()'s real apiFetch call resolves via the generic {}
// fallback in src/test/setup.ts, which is harmless but noisy (a thrown-then-
// caught TypeError inside the vars promise chain on every render).
vi.mock('../../../services/api');

beforeEach(() => {
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as Config);
});

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];

function entry(filePath: string): MemberEntry {
  return { filePath };
}

function renderRow(e: MemberEntry, aliases: PathAlias[] = []) {
  return render(
    <ThemeProvider theme={appTheme} defaultMode="dark">
      <MemberRow entry={e} book={undefined} pathAliases={aliases} />
    </ThemeProvider>,
  );
}

describe('MemberRow / PathLinks wiring', () => {
  it('renders a monospace path row with its copy button when entry.filePath is set', () => {
    renderRow(entry('/library/books/a.m4b'));
    const copyButton = screen.getByRole('button', { name: 'Copy Linux path' });
    expect(copyButton).toBeInTheDocument();
    const pathText = screen.getByText('/library/books/a.m4b');
    expect(getComputedStyle(pathText).fontFamily).toBe('monospace');
  });

  it('threads pathAliases through so the Windows and UNC rows actually render', () => {
    // The POSIX row renders even with an empty aliases array (see the test
    // above), so a test that only checked for the POSIX row would still pass
    // if `pathAliases` never made it from MemberRow's prop to PathLinks --
    // that is exactly the regression this test exists to catch. Only the
    // Windows/UNC rows are proof the alias actually matched and was used.
    renderRow(entry('/library/books/Author/Title/x.m4b'), ALIASES);
    expect(screen.getByRole('button', { name: 'Copy Windows path' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy UNC path' })).toBeInTheDocument();
  });

  it('renders no path row, and does not crash, when entry.filePath is falsy', () => {
    renderRow(entry(''), ALIASES);
    expect(screen.queryByRole('button', { name: /copy/i })).not.toBeInTheDocument();
  });

  it('does not throw clicking copy without a real clipboard or a mounted ToastProvider', async () => {
    // No ToastProvider wraps this render -- useToast() falls back to the
    // no-op { toast: () => {} } from its default context (ToastProvider.tsx),
    // and jsdom does not implement navigator.clipboard, so this exercises
    // both fallbacks at once.
    const user = userEvent.setup();
    renderRow(entry('/library/books/a.m4b'));
    await user.click(screen.getByRole('button', { name: 'Copy Linux path' }));
    // Reaching here without an uncaught rejection/throw is the assertion.
    expect(screen.getByRole('button', { name: 'Copy Linux path' })).toBeInTheDocument();
  });
});
