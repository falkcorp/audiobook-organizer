// file: web/src/components/review/ReviewWorkspace.heldSelection.test.tsx
// version: 1.0.0
// guid: 8b3e5f21-7c4a-4d96-a0e2-5f1d9c7b3a84
// last-edited: 2026-10-19
//
// A selection whose check against the current filters failed is held, and a
// held selection must reach NO command that acts on it. The palette disables
// those commands; this file calls their `run` handlers directly (a disabled
// menu item cannot be clicked, so the handler's own refusal is unreachable
// from the DOM) and asserts nothing is sent: "Write details into audio files"
// rewrites files, "Search again for selected" spends provider quota.

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { serveReviewList } from '../../test/reviewServer';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';
import type { CommandMenu, ReviewCommand } from './CommandBar';

vi.mock('../../services/api');

const captured: { menus: CommandMenu[] } = { menus: [] };
vi.mock('./CommandBar', () => ({
  CommandBar: ({ menus }: { menus: CommandMenu[] }) => {
    captured.menus = menus;
    return null;
  },
}));

function command(id: string): ReviewCommand {
  for (const m of captured.menus) {
    const groups = [...(m.simple ?? []), ...(m.advanced ?? [])];
    const all = [...(m.commands ?? []), ...groups.flatMap((g) => g.commands)];
    const hit = all.find((c) => c.id === id);
    if (hit) return hit;
  }
  throw new Error(`no command ${id}`);
}

function makeResult(id: string) {
  return {
    book: { id, title: `Book ${id}`, language: 'en' },
    status: 'matched',
    candidate: { source: 'audible', title: `Cand ${id}`, author: 'A', narrator: 'N', score: 2.0, language: 'en' },
  } as unknown as api.CandidateResult;
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  const results = [makeResult('a'), makeResult('b')];
  serveReviewList({ results, total_count: 2, matched: 2, no_match: 0, errors: 0, stale: 0 });
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getReviewItems).mockResolvedValue({ items: [], count: 0, limit: 500, offset: 0, total: 0 });
  vi.mocked(api.getReviewCount).mockResolvedValue({ applying: 0, needs_attention: 0, count: 0, by_kind: {} });
  vi.mocked(api.batchFetchCandidates).mockResolvedValue({ operation_id: 'op-1' });
  vi.mocked(api.batchWriteBackMetadata).mockResolvedValue({ operation_id: 'op-2' } as never);
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
});

describe('a held selection and the palette commands', () => {
  it('Write details into audio files and Search again for selected refuse while the selection is held', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/review']}>
        <ToastProvider>
          <ReviewWorkspace />
        </ToastProvider>
      </MemoryRouter>
    );
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    await user.click(screen.getByLabelText('Select Book a'));
    // Settled: the commands run.
    expect(command('write-back').disabledReason).toBeUndefined();

    // The Title change asks the server about the selection; the check fails.
    vi.mocked(api.checkReviewSelection).mockRejectedValue(
      Object.assign(new Error('failed to query the review set'), { status: 500, data: {} })
    );
    await user.click(screen.getByLabelText('Title filter'));
    await user.paste('Book');
    await waitFor(() =>
      expect(command('write-back').disabledReason).toMatch(/^Actions on the selection are held/)
    );
    expect(command('bulk-search-selected').disabledReason).toMatch(/^Actions on the selection are held/);

    await command('write-back').run();
    await command('bulk-search-selected').run();
    expect(api.batchWriteBackMetadata).not.toHaveBeenCalled();
    expect(api.batchFetchCandidates).not.toHaveBeenCalled();
  });
});
