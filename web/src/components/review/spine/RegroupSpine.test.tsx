// file: web/src/components/review/spine/RegroupSpine.test.tsx
// version: 1.3.0
// guid: 3f7c9e21-5a4b-4d8e-9c1f-2b6a7d0e4c53
// last-edited: 2026-10-18

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
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from '@mui/material/styles';
import { ApplyingAlert, MemberRow, SkipsAlert } from './RegroupSpine';
import type { RegroupLane } from '../lanes/useRegroupLane';
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
      <MemberRow entry={e} book={undefined} pathAliases={aliases} pathVars={[]} />
    </ThemeProvider>
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

describe('SkipsAlert report panel', () => {
  const KIND = 'regroup.multidisc';

  function renderAlert(lane: Partial<RegroupLane>) {
    const full = {
      skipsByKind: {},
      failedByKind: {},
      unrecordedByKind: {},
      dismissSkips: vi.fn(),
      ...lane,
    } as unknown as RegroupLane;
    render(
      <ThemeProvider theme={appTheme} defaultMode="dark">
        <div data-testid="regroup-row-f1">row f1</div>
        <SkipsAlert kind={KIND} lane={full} />
      </ThemeProvider>
    );
    return full;
  }

  it('renders nothing when the kind has no skips, failures or unrecorded items', () => {
    renderAlert({});
    expect(screen.queryByTestId(`regroup-skips-${KIND}`)).toBeNull();
  });

  it('lists failed items as clickable entries and unrecorded ones as plain ids, outliving the toast', async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    const lane = renderAlert({
      failedByKind: { [KIND]: [{ id: 'f1', reason: 'synthetic refusal' }] },
      unrecordedByKind: { [KIND]: [{ id: 'u1', reason: 'applied but status write failed' }] },
    });

    expect(screen.getByTestId(`regroup-failed-${KIND}`)).toHaveTextContent('synthetic refusal');
    expect(screen.getByTestId(`regroup-unrecorded-${KIND}`)).toHaveTextContent(
      'Approving one again records it without running the apply again'
    );
    // An unrecorded item is held mid-apply and is not in the list: no row to open.
    expect(screen.getByTestId('regroup-report-id-u1')).toBeInTheDocument();
    expect(screen.queryByTestId('regroup-report-open-u1')).toBeNull();

    await userEvent.click(screen.getByTestId('regroup-report-open-f1'));
    expect(scroll).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole('button', { name: /close/i }));
    expect(lane.dismissSkips).toHaveBeenCalledWith(KIND);
  });

  it('gives skips of applied or mid-apply items their own heading, not "still pending"', () => {
    renderAlert({
      skipsByKind: {
        [KIND]: [
          {
            id: 'p1',
            action: 'insufficient-evidence',
            reason: 'no decidable action',
            code: 'REVIEW_ACTION_NOT_DECIDABLE',
          },
          {
            id: 'd1',
            action: 'combine',
            reason: 'already applied',
            code: 'REVIEW_ALREADY_APPLIED',
          },
          {
            id: 'd2',
            action: 'combine',
            reason: 'an apply is in progress',
            code: 'REVIEW_APPLY_IN_PROGRESS',
          },
        ],
      },
    });
    const done = screen.getByTestId(`regroup-skipped-done-${KIND}`);
    expect(done).toHaveTextContent('2 holds were skipped — already applied or mid-apply');
    expect(done).not.toHaveTextContent('still pending');
    expect(screen.getByTestId('regroup-report-id-d1')).toBeInTheDocument();
    const pending = screen.getByTestId(`regroup-skipped-${KIND}`);
    expect(pending).toHaveTextContent('1 hold was skipped — still pending');
    expect(pending).not.toHaveTextContent('d1');
  });
});

describe('ApplyingAlert', () => {
  it('renders nothing when no hold is held mid-apply', () => {
    render(<ApplyingAlert count={0} onChanged={vi.fn()} />);
    expect(screen.queryByTestId('regroup-applying')).toBeNull();
  });

  it('lists the held holds on demand and finishes one with no explicit action', async () => {
    vi.mocked(api.getReviewItems).mockResolvedValue({
      items: [
        {
          id: 'h1',
          kind: 'regroup.multidisc',
          dedup_key: 'k',
          folder_ref: '/f',
          status: 'applying',
          summary: 'Chapters: 3 tracks',
          payload: '{}',
          created_at: '',
          updated_at: '',
          apply_claim_action: 'combine',
        },
      ],
      count: 1,
      total: 1,
      limit: 100,
      offset: 0,
    } as api.ReviewItemsPage);
    vi.mocked(api.approveReviewItem).mockResolvedValue({} as never);
    const onChanged = vi.fn();
    render(<ApplyingAlert count={1} onChanged={onChanged} />);

    expect(screen.getByTestId('regroup-applying')).toHaveTextContent('1 hold is held mid-apply');
    await userEvent.click(screen.getByTestId('regroup-applying-show'));
    expect(api.getReviewItems).toHaveBeenCalledWith({ status: 'applying', limit: 100 });
    await userEvent.click(await screen.findByTestId('regroup-applying-finish-h1'));
    // No action: the server resolves it to the claim's own action.
    expect(api.approveReviewItem).toHaveBeenCalledWith('h1');
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
  });
});
