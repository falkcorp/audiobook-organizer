// file: web/src/modules/books/pages/UserMergeIdentity.test.tsx
// version: 0.1.0
// guid: 32ba3e51-3823-4e84-92e9-d98094bec2db
// last-edited: 2026-11-22

// F4-08 B10l-ui: the merge preview under core identity. Core owns sign-ins and
// API keys, so books' two identity lists are empty; the page must say what core
// says moves instead of reading "no linked sign-ins" above a merge that moves some.
// The plan bodies mirror what the server sends under identity (core summary, an
// empty owner-identity holder, the acknowledgement required when a sign-in moves).

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../../../test/renderWithProviders';
import { UserMergeDialog } from './UserMerge';
import type { MergeableUser } from '../../../components/users/userAccountParts';
import type { UserMergeCoreSummary, UserMergePlan } from '../../../services/api';

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const absorbed: MergeableUser = { id: 'u-gone', username: 'first-account', status: 'active' };
const candidates: MergeableUser[] = [
  { id: 'u-stay', username: 'second-account', status: 'active', roles: ['admin'] },
];

const account = (id: string, username: string) => ({
  id,
  username,
  status: 'active',
  roles: ['admin'],
  has_password: true,
});

const makeCore = (over: Partial<UserMergeCoreSummary> = {}): UserMergeCoreSummary => ({
  roles_transferred: [],
  target_holds_admin: true,
  email: 'kept',
  api_key_ids: ['key-alfred', 'key-lucius'],
  identity_providers: ['gotham-sso'],
  allow_list_rows: 0,
  ...over,
});

// An identity-mode preview: no identity lists, core's summary, holder never
// checked (the empty string on the wire), the acknowledgement required.
const makeIdentityPlan = (
  core: Partial<UserMergeCoreSummary> = {},
  over: Partial<UserMergePlan> = {}
): UserMergePlan => ({
  survivor_id: 'u-stay',
  absorbed_id: 'u-gone',
  survivor: account('u-stay', 'second-account'),
  absorbed: account('u-gone', 'first-account'),
  counts: { book_state: { rows: 12, conflicts: 2 } },
  conflicts: [],
  conflict_total: 2,
  ops: 40,
  survivor_identities: [],
  absorbed_identities: [],
  core: makeCore(core),
  owner_identity_holder: '',
  owner_identity_on_absorbed: false,
  owner_identity_ack_required: true,
  roles_gained: [],
  email_outcome: 'the surviving account keeps its own email address',
  warnings: [],
  refusals: [],
  digest: 'digest-cccccccccccc',
  ...over,
});

// A HEAD-mode preview: no core, the two identity lists as HEAD sends them.
const makeHeadPlan = (over: Partial<UserMergePlan> = {}): UserMergePlan => ({
  ...makeIdentityPlan(),
  core: undefined,
  survivor_identities: [],
  absorbed_identities: [
    {
      id: 'i1',
      provider: 'oidc',
      subject: 'ab…yz',
      email: 'someone@example.test',
      linked_at: '2026-01-01T00:00:00Z',
      owner_email: false,
    },
  ],
  owner_identity_holder: 'survivor',
  owner_identity_ack_required: false,
  ...over,
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function openPreview(plan: UserMergePlan, onStarted = vi.fn()) {
  const calls: Array<{ url: string; body?: string }> = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url: String(url), body: init?.body as string | undefined });
      if (String(url).endsWith('/preview')) return jsonResponse({ data: plan });
      return jsonResponse({ data: { operation_id: 'op-9' } }, 202);
    })
  );
  const user = userEvent.setup();
  renderWithProviders(
    <UserMergeDialog
      absorbed={absorbed}
      candidates={candidates}
      onClose={() => {}}
      onStarted={onStarted}
    />
  );
  await user.click(screen.getByRole('combobox', { name: /account that stays/i }));
  await user.click(await screen.findByRole('option', { name: 'second-account' }));
  await screen.findByText('Books with listening progress');
  return { user, calls, onStarted };
}

describe('UserMergeDialog under core identity', () => {
  it('lists the providers that move, the key count and the allow-list count, and not the empty identity lists', async () => {
    await openPreview(
      makeIdentityPlan({
        identity_providers: ['gotham-sso', 'arkham-ldap'],
        api_key_ids: ['key-alfred', 'key-lucius', 'key-selina'],
        allow_list_rows: 2,
      })
    );

    expect(
      screen.getByText('Sign-ins that move to second-account: gotham-sso, arkham-ldap')
    ).toBeInTheDocument();
    expect(screen.getByText('API keys that move: 3')).toBeInTheDocument();
    expect(screen.getByText('Access allow-list entries that move: 2')).toBeInTheDocument();
    // The empty lists would read as "this account has no sign-ins".
    expect(screen.queryByText('No linked sign-ins')).not.toBeInTheDocument();
    expect(screen.queryByText('Stays: second-account')).not.toBeInTheDocument();
    expect(screen.queryByText('Merged away: first-account')).not.toBeInTheDocument();
    // The ids add nothing for the admin.
    expect(screen.queryByText(/key-alfred/)).not.toBeInTheDocument();
  });

  it('says none when no sign-in moves, counts zero keys for a null id list, and hides a zero allow-list', async () => {
    // Go marshals a nil slice as null, which is what core sends when nothing moves.
    await openPreview(
      makeIdentityPlan(
        { identity_providers: null, api_key_ids: null, allow_list_rows: 0 },
        { owner_identity_ack_required: false }
      )
    );

    expect(screen.getByText('Sign-ins that move to second-account: none')).toBeInTheDocument();
    expect(screen.getByText('API keys that move: 0')).toBeInTheDocument();
    expect(screen.queryByText(/allow-list/i)).not.toBeInTheDocument();
    expect(screen.queryByText('No linked sign-ins')).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /merge into second-account/i })).toBeEnabled();
  });

  it('says a sign-in moves and cannot be checked against the owner when the holder is unchecked, and needs the acknowledgement before Apply enables', async () => {
    const { user, calls, onStarted } = await openPreview(makeIdentityPlan());

    expect(
      screen.getByText(
        /A sign-in on the account being merged away moves to second-account, and this page cannot tell whether it is the owner.s\./
      )
    ).toBeInTheDocument();
    // An unchecked holder is not "no owner configured".
    expect(screen.queryByText(/No owner email is configured/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/cannot be checked against the owner address/i)).not.toBeInTheDocument();

    const merge = screen.getByRole('button', { name: /merge into second-account/i });
    expect(merge).toBeDisabled();
    await user.click(screen.getByRole('checkbox', { name: /owner.s sign-in moves/i }));
    expect(merge).toBeEnabled();
    await user.click(merge);

    await waitFor(() => expect(onStarted).toHaveBeenCalled());
    const apply = calls.find((c) => c.url.endsWith('/apply'));
    expect(JSON.parse(apply?.body ?? '{}')).toEqual({
      digest: 'digest-cccccccccccc',
      acknowledge_owner_identity: true,
    });
  });

  it('keeps rendering the roles, the e-mail outcome and the warnings core sends', async () => {
    await openPreview(
      makeIdentityPlan(
        { roles_transferred: ['editor'] },
        {
          roles_gained: ['editor'],
          warnings: ['the merged-away account has a verified address that is released'],
        }
      )
    );

    expect(screen.getByText('Roles the surviving account gains: editor')).toBeInTheDocument();
    expect(
      screen.getByText('Email: the surviving account keeps its own email address')
    ).toBeInTheDocument();
    expect(
      screen.getByText('the merged-away account has a verified address that is released')
    ).toBeInTheDocument();
  });
});

describe('UserMergeDialog without core (HEAD)', () => {
  it('renders the two identity lists and none of the core lines', async () => {
    await openPreview(makeHeadPlan());

    expect(screen.getByText('Stays: second-account')).toBeInTheDocument();
    expect(screen.getByText('Merged away: first-account')).toBeInTheDocument();
    expect(screen.getByText('No linked sign-ins')).toBeInTheDocument();
    expect(screen.getByText(/oidc · someone@example\.test · ab…yz/)).toBeInTheDocument();
    expect(screen.queryByText(/Sign-ins that move to/)).not.toBeInTheDocument();
    expect(screen.queryByText(/API keys that move/)).not.toBeInTheDocument();
    expect(screen.queryByText(/allow-list/i)).not.toBeInTheDocument();
  });

  it('keeps HEAD wording for a holder that cannot be checked', async () => {
    await openPreview(
      makeHeadPlan({ owner_identity_holder: '', owner_identity_ack_required: true })
    );

    expect(
      screen.getByText(
        /The account being merged away has a sign-in that cannot be checked against the owner address\. After the merge its sign-ins belong to second-account\./
      )
    ).toBeInTheDocument();
    expect(screen.queryByText(/this page cannot tell whether it is the owner/)).not.toBeInTheDocument();
  });
});
