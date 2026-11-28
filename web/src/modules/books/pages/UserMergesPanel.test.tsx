// file: web/src/modules/books/pages/UserMergesPanel.test.tsx
// version: 0.1.0
// guid: d60e18ef-5381-4a28-9847-0fa467b0b87a
// last-edited: 2026-11-22

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../../../test/renderWithProviders';
import { UserMergesPanel } from './UserMerge';
import {
  USER_MERGE_REFUSAL_TEXT,
  userMergeRefusalCode,
  userMergeRefusalMessage,
} from './userMergeRefusal';
import { ApiError } from '../../../services/api';
import type { MergeableUser } from '../../../components/users/userAccountParts';
import type { UserMergeJournal } from '../../../services/api';

// F4-08 B10p: the undo and resume of an account merge under core identity. The
// refusal codes are shown in words, the undo is held back while the straggler
// sweep is pending, and Resume is offered for exactly that case.

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const users: MergeableUser[] = [
  { id: 'u-stay', username: 'bruce-wayne', status: 'active', roles: ['admin'] },
  { id: 'u-gone', username: 'dick-grayson', status: 'disabled' },
];

const journal = (over: Partial<UserMergeJournal> = {}): UserMergeJournal => ({
  id: 'j1',
  status: 'applied',
  survivor_id: 'u-stay',
  absorbed_id: 'u-gone',
  ops: 3,
  cursor: 3,
  retired: false,
  conflict_total: 0,
  created_at: '2026-11-22T01:00:00Z',
  updated_at: '2026-11-22T01:00:00Z',
  ...over,
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubServer(journals: UserMergeJournal[], action?: (url: string) => Response | undefined) {
  const calls: string[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? 'GET'} ${String(url)}`);
      const forced = action?.(String(url));
      if (forced) return forced;
      if (String(url).endsWith('/users/merges'))
        return jsonResponse({ data: { merges: journals } });
      return jsonResponse({ data: { operation_id: 'op-1' } }, 202);
    })
  );
  return calls;
}

const renderPanel = () =>
  renderWithProviders(<UserMergesPanel users={users} refreshKey={0} onNotice={() => {}} />);

describe('UserMergesPanel undo and resume', () => {
  it('offers Undo for an applied merge and no Resume', async () => {
    stubServer([journal()]);
    renderPanel();
    expect(await screen.findByRole('button', { name: 'Undo' })).toBeEnabled();
    expect(screen.queryByRole('button', { name: 'Resume' })).not.toBeInTheDocument();
  });

  it('holds Undo back and offers Resume while the straggler sweep is pending', async () => {
    stubServer([journal({ sweep_pending: true })]);
    renderPanel();
    expect(await screen.findByRole('button', { name: 'Resume' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Undo' })).toBeDisabled();
    expect(screen.getByText(/resume it to finish, then it can be undone/i)).toBeInTheDocument();
  });

  it('offers neither for an undone merge, and Resume (not Undo-only) for an applying one', async () => {
    stubServer([
      journal({ id: 'j-undone', status: 'undone' }),
      journal({ id: 'j-applying', status: 'applying', cursor: 1 }),
    ]);
    renderPanel();
    await screen.findAllByRole('row');
    await waitFor(() => expect(screen.getAllByRole('button', { name: 'Undo' })).toHaveLength(1));
    expect(screen.getAllByRole('button', { name: 'Resume' })).toHaveLength(1);
  });

  it('posts the resume of a pending sweep to the resume route', async () => {
    const calls = stubServer([journal({ sweep_pending: true })]);
    const user = userEvent.setup();
    renderPanel();
    await user.click(await screen.findByRole('button', { name: 'Resume' }));
    await waitFor(() => expect(calls).toContain('POST /api/v1/users/merges/j1/resume'));
  });

  it('posts the undo after the confirmation and says a guest must be reissued', async () => {
    const calls = stubServer([journal()]);
    const user = userEvent.setup();
    renderPanel();
    await user.click(await screen.findByRole('button', { name: 'Undo' }));
    expect(
      await screen.findByText(/Cloudflare access and phone key are not restored/i)
    ).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Undo merge' }));
    await waitFor(() => expect(calls).toContain('POST /api/v1/users/merges/j1/undo'));
  });

  it.each(Object.keys(USER_MERGE_REFUSAL_TEXT))(
    'shows the refusal %s in words, not as a code',
    async (code) => {
      stubServer([journal()], (url) =>
        url.endsWith('/undo')
          ? jsonResponse({ error: `raw server text for ${code}`, code, status: 409 }, 409)
          : undefined
      );
      const user = userEvent.setup();
      renderPanel();
      await user.click(await screen.findByRole('button', { name: 'Undo' }));
      await user.click(screen.getByRole('button', { name: 'Undo merge' }));
      const alert = await screen.findByRole('alert');
      expect(alert).toHaveTextContent(USER_MERGE_REFUSAL_TEXT[code]);
      expect(alert).not.toHaveTextContent(code);
    }
  );

  it("shows the server's own sentence on a 403 (dominance or denied)", async () => {
    stubServer([journal()], (url) =>
      url.endsWith('/undo')
        ? jsonResponse(
            { error: 'the account being merged is held by an admin who outranks you', status: 403 },
            403
          )
        : undefined
    );
    const user = userEvent.setup();
    renderPanel();
    await user.click(await screen.findByRole('button', { name: 'Undo' }));
    await user.click(screen.getByRole('button', { name: 'Undo merge' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('held by an admin who outranks you');
  });
});

describe('userMergeRefusalMessage', () => {
  it('has a sentence for every code the routes answer with', () => {
    for (const code of [
      'not_merged',
      'source_changed',
      'source_deleted',
      'target_deleted',
      'target_inactive',
      'target_merged',
      'email_claimed',
      'key_moved',
      'signin_moved',
      'sweep_pending',
      'receipt_missing',
      'merge_permanent',
      'last_admin',
      'conflict',
    ]) {
      expect(USER_MERGE_REFUSAL_TEXT[code], code).toBeTruthy();
    }
  });

  it('tells the admin to run resume first for sweep_pending', () => {
    expect(USER_MERGE_REFUSAL_TEXT.sweep_pending).toMatch(/Resume/);
  });

  it('gives every code its own sentence', () => {
    const sentences = Object.values(USER_MERGE_REFUSAL_TEXT);
    expect(new Set(sentences).size).toBe(sentences.length);
  });

  it('falls back to the server text for a code with no sentence, and to the default otherwise', () => {
    expect(
      userMergeRefusalMessage(new ApiError('server says no', 409, { code: 'new_code' }), 'x')
    ).toBe('server says no');
    expect(userMergeRefusalMessage('not an error', 'the default')).toBe('the default');
    expect(userMergeRefusalCode(new ApiError('m', 409, { code: 'key_moved' }))).toBe('key_moved');
    expect(userMergeRefusalCode(new Error('m'))).toBe('');
  });
});
