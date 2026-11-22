// file: web/src/pages/AcceptInvite.phoneClaim.test.tsx
// version: 0.1.0
// guid: c8530fec-e9f0-4fa3-8269-9c9e3fa62594
// last-edited: 2026-11-22

// The guest phone-key claim on /invite (F4-08 B10s contract): after core's accept succeeds the page
// POSTs /api/v1/auth/phone-key/claim once, same origin, with credentials, no body and no query. The
// secret is shown once, held in state only, and a failed claim never makes the accept an error.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { coreJson, coreProblem, stubCoreFetch, type CoreHandler } from '../test/coreFetch';
import { PHONE_KEY_CLAIM_PATH, parsePhoneApp } from '../services/phoneKeyClaim';
import { AcceptInvite } from './AcceptInvite';

const TOKEN = 'mcl_' + 'b'.repeat(40);
const CLAIM = `POST ${PHONE_KEY_CLAIM_PATH}`;

const ISSUED = {
  status: 'issued',
  client_id: 'batcave.access',
  client_secret: 'batcave-secret-value',
  id_header: 'CF-Access-Client-Id',
  secret_header: 'CF-Access-Client-Secret',
};

function serve(claim: CoreHandler | Response | (() => Response), extra: Record<string, CoreHandler | Response | (() => Response)> = {}) {
  return stubCoreFetch({
    'POST /auth/invites/lookup': () =>
      coreJson({ username: 'alfred', role: 'viewer', expires_at: '2026-10-22T12:00:00Z' }),
    'POST /auth/invites/accept': () => coreJson({ public_id: 's1', expires_at: '2026-10-23T12:00:00Z' }),
    [CLAIM]: claim,
    ...extra,
  });
}

async function acceptInvite(user: ReturnType<typeof userEvent.setup>) {
  window.history.replaceState(null, '', `/invite#token=${TOKEN}`);
  renderWithProviders(<AcceptInvite />, { initialEntries: ['/invite'] });
  await user.type(await screen.findByLabelText(/choose a password/i), 'long-enough-pw');
  await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
  await user.click(screen.getByRole('button', { name: /create my account/i }));
  expect(await screen.findByText(/is ready and you are signed in/i)).toBeInTheDocument();
}

const claimCalls = (calls: { path: string }[]) => calls.filter((c) => c.path === PHONE_KEY_CLAIM_PATH);

beforeEach(() => window.history.replaceState(null, '', '/'));
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('AcceptInvite phone-key claim', () => {
  it('claims once after accept, same origin with credentials and no body, and shows the secret once', async () => {
    const { calls, fetchMock } = serve(() => coreJson({ phone_app: ISSUED }));
    await acceptInvite(userEvent.setup());

    expect(await screen.findByTestId('phone-app-client-id')).toHaveTextContent('batcave.access');
    expect(screen.getByTestId('phone-app-client-secret')).toHaveTextContent('batcave-secret-value');
    expect(screen.getByText('CF-Access-Client-Id')).toBeInTheDocument();
    expect(screen.getByText('CF-Access-Client-Secret')).toBeInTheDocument();
    expect(screen.getByText(/will not be shown again/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy client ID' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy client secret' })).toBeInTheDocument();

    // exactly one claim, after the accept, with no body and no query
    expect(claimCalls(calls)).toHaveLength(1);
    expect(claimCalls(calls)[0]).toMatchObject({ method: 'POST', body: null });
    const order = calls.map((c) => c.path);
    expect(order.indexOf('/auth/invites/accept')).toBeLessThan(order.indexOf(PHONE_KEY_CLAIM_PATH));
    const claimFetch = fetchMock.mock.calls.find(([input]) => String(input) === PHONE_KEY_CLAIM_PATH);
    expect(claimFetch?.[1]).toMatchObject({ method: 'POST', credentials: 'same-origin' });
    expect(claimFetch?.[1]?.body).toBeUndefined();
  });

  it('copies the secret to the clipboard on request', async () => {
    serve(() => coreJson({ phone_app: ISSUED }));
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    await acceptInvite(user);
    await user.click(await screen.findByRole('button', { name: 'Copy client secret' }));
    expect(writeText).toHaveBeenCalledWith('batcave-secret-value');
  });

  it('parses both envelopes: the bare object and RespondWithOK wrapping it in data', async () => {
    expect(parsePhoneApp({ phone_app: ISSUED })).toEqual(ISSUED);
    expect(parsePhoneApp({ data: { phone_app: ISSUED } })).toEqual(ISSUED);
    expect(parsePhoneApp({ data: { nothing: true } })).toBeNull();
    expect(parsePhoneApp(null)).toBeNull();
    expect(parsePhoneApp({ phone_app: { status: 'issued', client_id: 'half-a-key' } })).toBeNull();

    serve(() => coreJson({ data: { phone_app: ISSUED } }));
    await acceptInvite(userEvent.setup());
    expect(await screen.findByTestId('phone-app-client-secret')).toHaveTextContent('batcave-secret-value');
  });

  it('shows the message and the reissue instruction when the key is unavailable', async () => {
    serve(() => coreJson({ phone_app: { status: 'unavailable', message: 'The key service is not set up.' } }));
    await acceptInvite(userEvent.setup());
    expect(await screen.findByText(/the key service is not set up/i)).toBeInTheDocument();
    expect(screen.getByText(/ask the administrator to reissue your phone-app key/i)).toBeInTheDocument();
    expect(screen.queryByTestId('phone-app-client-secret')).not.toBeInTheDocument();
  });

  it('shows no phone-key UI on 404 PHONE_KEY_CLAIM_REFUSED (a link-only invite)', async () => {
    const { calls } = serve(() => coreProblem(404, 'PHONE_KEY_CLAIM_REFUSED', 'no key for this invite'));
    await acceptInvite(userEvent.setup());
    await waitFor(() => expect(claimCalls(calls)).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole('button', { name: /continue to the library/i })).toBeEnabled());
    expect(screen.queryByText(/phone-app key|phone app/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/CF-Access-Client/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/no key for this invite/i)).not.toBeInTheDocument();
  });

  it.each([
    ['401', () => coreProblem(401, 'unauthorized', 'sign in')],
    ['403', () => coreProblem(403, 'forbidden', 'no')],
    ['429', () => coreProblem(429, 'rate_limited', 'slow down')],
    ['500', () => coreProblem(500, 'internal', 'boom')],
    ['a network error', () => Promise.reject(new TypeError('Failed to fetch')) as unknown as Response],
    ['a body that is not JSON', () => new Response('<html>', { status: 200 })],
  ])('a failed claim (%s) leaves the accept successful, shows no phone-key UI, and is not retried', async (_name, answer) => {
    const { calls } = serve(answer as () => Response);
    await acceptInvite(userEvent.setup());
    await waitFor(() => expect(screen.getByRole('button', { name: /continue to the library/i })).toBeEnabled());
    expect(claimCalls(calls)).toHaveLength(1);
    expect(screen.queryByText(/could not|failed|error/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/phone-app key|CF-Access-Client/i)).not.toBeInTheDocument();
    expect(screen.getByText(/is ready and you are signed in/i)).toBeInTheDocument();
  });

  it('claims nothing when the accept fails', async () => {
    const { calls } = serve(() => coreJson({ phone_app: ISSUED }), {
      'POST /auth/invites/accept': coreProblem(422, 'weak_password', 'that password is too common'),
    });
    window.history.replaceState(null, '', `/invite#token=${TOKEN}`);
    renderWithProviders(<AcceptInvite />, { initialEntries: ['/invite'] });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/choose a password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    expect(await screen.findByText('that password is too common')).toBeInTheDocument();
    expect(claimCalls(calls)).toHaveLength(0);
  });

  it('writes nothing to web storage, the URL or the console', async () => {
    const local = vi.spyOn(Storage.prototype, 'setItem');
    const logs = [vi.spyOn(console, 'log'), vi.spyOn(console, 'info'), vi.spyOn(console, 'warn'), vi.spyOn(console, 'error')];
    const { fetchMock } = serve(() => coreJson({ phone_app: ISSUED }));
    await acceptInvite(userEvent.setup());
    await screen.findByTestId('phone-app-client-secret');
    expect(local).not.toHaveBeenCalled();
    for (const spy of logs) {
      expect(spy.mock.calls.flat().map(String).join(' ')).not.toContain('batcave-secret-value');
    }
    expect(window.location.href).not.toContain('batcave-secret-value');
    for (const [input] of fetchMock.mock.calls) {
      expect(String(input instanceof Request ? input.url : input)).not.toContain('batcave-secret-value');
    }
  });

  it('drops the secret from the screen when the page unmounts', async () => {
    serve(() => coreJson({ phone_app: ISSUED }));
    window.history.replaceState(null, '', `/invite#token=${TOKEN}`);
    const view = renderWithProviders(<AcceptInvite />, { initialEntries: ['/invite'] });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/choose a password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    await screen.findByTestId('phone-app-client-secret');
    view.unmount();
    expect(document.body.textContent).not.toContain('batcave-secret-value');
  });
});
