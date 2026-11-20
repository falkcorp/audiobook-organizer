// file: core/web/packages/shell/test/auth.flow.test.tsx
// version: 0.1.0
// guid: b27a342c-3ad8-4850-9fe7-3a8be30da7e6
// last-edited: 2026-11-20

import { createTheme, ThemeProvider } from '@mui/material/styles';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createShellClient, shellOperations, type ShellClient } from '../src/api/interim';
import {
  AuthProvider,
  InvitePage,
  LoginPage,
  ModulesHintsContext,
  RequireAuth,
  SetupPage,
  TempLoginPage,
  safeReturn,
  useAction,
  useAuth,
  useAuthClient,
  usePermission,
} from '../src/auth';
import type { ModuleUI, Permission } from '../src/types';

// ---- a fake server ----------------------------------------------------------

const API = '/api/core/v1';

interface Call {
  method: string;
  path: string;
  url: string;
  body: unknown;
}

type Handler = (call: Call, req: Request) => Response | Promise<Response>;

function json(status: number, body?: unknown): Response {
  if (status === 204) return new Response(null, { status });
  return new Response(JSON.stringify(body ?? {}), { status, headers: { 'Content-Type': 'application/json' } });
}

function problem(status: number, code: string, detail: string): Response {
  const body = { type: `https://mediacore.dev/problems/${code}`, title: 'Problem', status, code, detail, request_id: 'req-1' };
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/problem+json' } });
}

const STATUS_OK = { has_users: true, auth_disabled: false, setup_required: false };
const NOT_SIGNED_IN = () => problem(401, 'authentication_required', 'sign in to use this route');

function me(permissions: string[] = []) {
  return json(200, {
    user: { id: 'u-1', username: 'bruce', roles: ['admin'], status: 'active' },
    permissions,
    method: 'session',
    owner_verified: false,
  });
}

/** `routes` maps "METHOD /path" (without the API prefix) to a handler; an unlisted request fails the test. */
function fakeServer(routes: Record<string, Handler>) {
  const calls: Call[] = [];
  const fetchFn: typeof fetch = async (input, init) => {
    const req = input instanceof Request ? input : new Request(input, init);
    const url = new URL(req.url);
    const text = await req.clone().text();
    const call: Call = { method: req.method, path: url.pathname, url: req.url, body: text ? JSON.parse(text) : undefined };
    calls.push(call);
    const handler = routes[`${req.method} ${url.pathname.replace(API, '')}`];
    if (!handler) throw new Error(`unexpected request ${req.method} ${url.pathname}`);
    return handler(call, req);
  };
  return { fetch: fetchFn, calls, count: (key: string) => calls.filter((c) => `${c.method} ${c.path.replace(API, '')}` === key).length };
}

function Probe() {
  const client = useAuthClient();
  return (
    <button
      onClick={() => {
        void client.GET('/api/core/v1/settings');
      }}
    >
      Reload settings
    </button>
  );
}

function renderApp(initial: string, routes: Record<string, Handler>, opts: { onSignedOut?: () => void; library?: ReactNode } = {}) {
  const server = fakeServer(routes);
  const client: ShellClient = createShellClient({ buildId: 'test', shell: true, fetch: server.fetch });
  const router = createMemoryRouter(
    [
      { path: '/login', element: <LoginPage /> },
      { path: '/login/temp', element: <TempLoginPage /> },
      { path: '/setup', element: <SetupPage /> },
      { path: '/invite', element: <InvitePage /> },
      { path: '/books', element: <RequireAuth>{opts.library ?? <div>Library</div>}</RequireAuth> },
      { path: '/', element: <RequireAuth><div>Home</div></RequireAuth> },
    ],
    { initialEntries: [initial] },
  );
  const view = render(
    <ThemeProvider theme={createTheme()}>
      <AuthProvider client={client} onSignedOut={opts.onSignedOut}>
        <RouterProvider router={router} />
      </AuthProvider>
    </ThemeProvider>,
  );
  return { ...view, server, router, client };
}

beforeEach(() => {
  window.history.replaceState(null, '', '/');
});
afterEach(() => {
  vi.restoreAllMocks();
});

// ---- tests ------------------------------------------------------------------

describe('anonymous visitor and login', () => {
  it('goes to /login with the page they wanted, posts the login body and lands on the return path', async () => {
    let signedIn = false;
    const onSignedOut = vi.fn();
    const { server, router } = renderApp(
      '/books',
      {
        'GET /auth/status': () => json(200, STATUS_OK),
        'GET /me': () => (signedIn ? me() : NOT_SIGNED_IN()),
        'POST /auth/login': () => {
          signedIn = true;
          return json(200, { public_id: 'sess-1', expires_at: '2030-01-01T00:00:00Z' });
        },
      },
      { onSignedOut },
    );

    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/login');
    expect(router.state.location.search).toBe('?return=%2Fbooks');

    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/username/i), 'bruce');
    await user.type(screen.getByLabelText(/^password/i), 'wayne-manor-1939');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByText('Library')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/books');
    expect(server.calls.find((c) => c.method === 'POST')?.body).toEqual({ username: 'bruce', password: 'wayne-manor-1939', remember: false });
    expect(server.count('GET /auth/status')).toBe(2); // the mount probe, then one refresh after login
    // The anonymous probe's own 401 is not a sign-out.
    expect(onSignedOut).not.toHaveBeenCalled();
  });

  it('shows a wrong password in place, without leaving /login', async () => {
    const onSignedOut = vi.fn();
    const { router } = renderApp(
      '/login',
      {
        'GET /auth/status': () => json(200, STATUS_OK),
        'GET /me': NOT_SIGNED_IN,
        'POST /auth/login': () => problem(401, 'invalid_credentials', 'the username or password is wrong'),
      },
      { onSignedOut },
    );
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/username/i), 'bruce');
    await user.type(screen.getByLabelText(/^password/i), 'not-it');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByText('the username or password is wrong')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/login');
    expect(onSignedOut).not.toHaveBeenCalled();
  });

  it.each(['//192.0.2.1', 'https://192.0.2.1/path', '/\\192.0.2.1'])('lands on / after login when the return is %s', async (unsafe) => {
    let signedIn = false;
    const { router } = renderApp(`/login?return=${encodeURIComponent(unsafe)}`, {
      'GET /auth/status': () => json(200, STATUS_OK),
      'GET /me': () => (signedIn ? me() : NOT_SIGNED_IN()),
      'POST /auth/login': () => {
        signedIn = true;
        return json(200, { public_id: 'sess-1', expires_at: '2030-01-01T00:00:00Z' });
      },
    });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/username/i), 'bruce');
    await user.type(screen.getByLabelText(/^password/i), 'wayne-manor-1939');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByText('Home')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/');
  });
});

describe('safeReturn', () => {
  it.each([
    [null, '/'],
    ['', '/'],
    ['books', '/'],
    ['//192.0.2.1', '/'],
    ['https://192.0.2.1/x', '/'],
    ['/\\192.0.2.1', '/'],
    ['//192.0.2.1/evil', '/'],
    ['/\\192.0.2.1/evil', '/'],
    ['/books\n/x', '/'],
    // Dot segments normalize to a pathname starting with "//" (scheme-relative on navigation).
    ['/.//192.0.2.1', '/'],
    ['/..//192.0.2.1', '/'],
    ['/%2e//192.0.2.1', '/'],
    ['/%2E%2E//192.0.2.1/evil', '/'],
    ['/a/..//192.0.2.1', '/'],
    ['/%5c192.0.2.1', '/'],
    ['/%5C192.0.2.1', '/'],
    ['/\t/192.0.2.1', '/'],
    ['/books\t', '/'],
    ['/books\r\n', '/'],
    ['/./books', '/books'],
    ['/books', '/books'],
    ['/books/42?tab=files#top', '/books/42?tab=files#top'],
  ])('%j -> %j', (raw, want) => {
    expect(safeReturn(raw)).toBe(want);
  });
});

describe('setup', () => {
  it('routes to /setup while no user exists, without asking /me', async () => {
    const { router, server } = renderApp('/books', {
      'GET /auth/status': () => json(200, { has_users: false, auth_disabled: false, setup_required: true }),
    });
    expect(await screen.findByRole('heading', { name: 'Set up' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/setup');
    expect(server.count('GET /me')).toBe(0);
  });

  it('sends the bootstrap token with the account, then signs in', async () => {
    let setupDone = false;
    let signedIn = false;
    const { server, router } = renderApp('/setup', {
      'GET /auth/status': () =>
        json(200, setupDone ? STATUS_OK : { has_users: false, auth_disabled: false, setup_required: true }),
      'GET /me': () => (signedIn ? me() : NOT_SIGNED_IN()),
      'POST /auth/setup': () => {
        setupDone = true;
        return json(200, { id: 'u-1', username: 'bruce' });
      },
      'POST /auth/login': () => {
        signedIn = true;
        return json(200, { public_id: 'sess-1', expires_at: '2030-01-01T00:00:00Z' });
      },
    });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/bootstrap token/i), 'tok-batcave');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.type(screen.getByLabelText(/username/i), 'bruce');
    await user.type(screen.getByLabelText(/^password/i), 'wayne-manor-1939');
    await user.type(screen.getByLabelText(/confirm password/i), 'wayne-manor-1939');
    await user.click(screen.getByRole('button', { name: 'Create account' }));

    expect(await screen.findByText('Home')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/');
    const posts = server.calls.filter((c) => c.method === 'POST');
    expect(posts[0].path).toBe(`${API}/auth/setup`);
    expect(posts[0].body).toEqual({ username: 'bruce', password: 'wayne-manor-1939', bootstrap_token: 'tok-batcave' });
    expect(posts[1].body).toEqual({ username: 'bruce', password: 'wayne-manor-1939' });
  });

  it('returns to the token step when the server refuses the bootstrap token, and shows why', async () => {
    renderApp('/setup', {
      'GET /auth/status': () => json(200, { has_users: false, auth_disabled: false, setup_required: true }),
      'POST /auth/setup': () => problem(401, 'bootstrap_invalid', 'the bootstrap token is wrong, expired or spent'),
    });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/bootstrap token/i), 'tok-wrong');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.type(screen.getByLabelText(/username/i), 'bruce');
    await user.type(screen.getByLabelText(/^password/i), 'wayne-manor-1939');
    await user.type(screen.getByLabelText(/confirm password/i), 'wayne-manor-1939');
    await user.click(screen.getByRole('button', { name: 'Create account' }));

    expect(await screen.findByText('the bootstrap token is wrong, expired or spent')).toBeInTheDocument();
    expect(screen.getByLabelText(/bootstrap token/i)).toBeInTheDocument();
  });
});

describe('one-time tokens in the URL fragment', () => {
  it('reads the invite token, removes it with replaceState and sends it in the body', async () => {
    window.history.replaceState({ routerKey: 'keep' }, '', '/invite#token=batcave-invite-1');
    const replaceState = vi.spyOn(window.history, 'replaceState');
    let signedIn = false;
    const { server, router } = renderApp('/invite', {
      'GET /auth/status': () => json(200, STATUS_OK),
      'GET /me': () => (signedIn ? me() : NOT_SIGNED_IN()),
      'POST /auth/invites/lookup': () => json(200, { username: 'alfred', role: 'editor', expires_at: '2030-01-01T00:00:00Z' }),
      'POST /auth/invites/accept': () => {
        signedIn = true;
        return json(200, { public_id: 'sess-2', expires_at: '2030-01-01T00:00:00Z' });
      },
    });

    expect(await screen.findByText('alfred')).toBeInTheDocument();
    expect(window.location.hash).toBe('');
    expect(window.history.state).toEqual({ routerKey: 'keep' });
    expect(replaceState).toHaveBeenCalled();
    expect(String(replaceState.mock.calls.at(-1)?.[2])).not.toContain('token');

    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/^password/i), 'pennyworth-1');
    await user.type(screen.getByLabelText(/confirm password/i), 'pennyworth-1');
    await user.click(screen.getByRole('button', { name: 'Accept invite' }));

    expect(await screen.findByText('Home')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/');
    const posts = server.calls.filter((c) => c.method === 'POST');
    expect(posts[0].body).toEqual({ token: 'batcave-invite-1' });
    expect(posts[1].body).toEqual({ token: 'batcave-invite-1', password: 'pennyworth-1' });
    // The token never travels in a URL.
    expect(server.calls.every((c) => !c.url.includes('batcave-invite-1'))).toBe(true);
  });

  it('shows an empty state for an invite link with no token', async () => {
    renderApp('/invite', { 'GET /auth/status': () => json(200, STATUS_OK), 'GET /me': NOT_SIGNED_IN });
    expect(await screen.findByText(/missing its token/i)).toBeInTheDocument();
  });

  it('shows why a dead invite is dead', async () => {
    window.history.replaceState(null, '', '/invite#token=spent');
    renderApp('/invite', {
      'GET /auth/status': () => json(200, STATUS_OK),
      'GET /me': NOT_SIGNED_IN,
      'POST /auth/invites/lookup': () => problem(401, 'link_invalid', 'the link is not valid'),
    });
    expect(await screen.findByText('the link is not valid')).toBeInTheDocument();
  });

  it('spends a temp-login link with the token alone, and with a new password only when one is typed', async () => {
    window.history.replaceState(null, '', '/login/temp#token=batcave-temp-1');
    let signedIn = false;
    const { server, router } = renderApp('/login/temp', {
      'GET /auth/status': () => json(200, STATUS_OK),
      'GET /me': () => (signedIn ? me() : NOT_SIGNED_IN()),
      'POST /auth/temp-login/consume': (c) => {
        if (!(c.body as { new_password?: string }).new_password) return problem(400, 'bad_request', 'this is a reset link: choose a new password');
        signedIn = true;
        return json(200, { public_id: 'sess-3', expires_at: '2030-01-01T00:00:00Z' });
      },
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Continue' }));
    expect(await screen.findByText('this is a reset link: choose a new password')).toBeInTheDocument();
    expect(window.location.hash).toBe('');

    await user.type(screen.getByLabelText(/new password/i), 'pennyworth-2');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    expect(await screen.findByText('Home')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/');
    const posts = server.calls.filter((c) => c.method === 'POST');
    expect(posts[0].body).toEqual({ token: 'batcave-temp-1' });
    expect(posts[1].body).toEqual({ token: 'batcave-temp-1', new_password: 'pennyworth-2' });
  });
});

describe('the 401 rule', () => {
  const signedInRoutes = (settings: Handler): Record<string, Handler> => ({
    'GET /auth/status': () => json(200, STATUS_OK),
    'GET /me': () => me(),
    'GET /settings': settings,
  });

  it('signs out to /login?return= on a session-ending 401 from a non-public call', async () => {
    const onSignedOut = vi.fn();
    const { router } = renderApp('/books', signedInRoutes(() => problem(401, 'expired', 'the session expired')), {
      onSignedOut,
      library: <Probe />,
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Reload settings' }));

    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/login');
    expect(router.state.location.search).toBe('?return=%2Fbooks');
    expect(onSignedOut).toHaveBeenCalledTimes(1);
  });

  it('logout() posts, signs out and reports it', async () => {
    const onSignedOut = vi.fn();
    function LogoutButton() {
      const auth = useAuth();
      return <button onClick={() => void auth.logout()}>Log out</button>;
    }
    const { server, router } = renderApp(
      '/books',
      { 'GET /auth/status': () => json(200, STATUS_OK), 'GET /me': () => me(), 'POST /auth/logout': () => json(204) },
      { onSignedOut, library: <LogoutButton /> },
    );
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Log out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument();
    expect(server.count('POST /auth/logout')).toBe(1);
    expect(router.state.location.pathname).toBe('/login');
    expect(onSignedOut).toHaveBeenCalledTimes(1);
  });
});

describe('a failed probe', () => {
  it('renders the server error and a working retry, not an endless spinner', async () => {
    let healthy = false;
    renderApp('/books', {
      'GET /auth/status': () => (healthy ? json(200, STATUS_OK) : problem(500, 'internal', 'the vault is sealed')),
      'GET /me': () => me(),
    });
    expect(await screen.findByText('the vault is sealed')).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();

    healthy = true;
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('Library')).toBeInTheDocument();
  });

  it('renders a network failure with its message', async () => {
    renderApp('/books', {
      'GET /auth/status': () => {
        throw new TypeError('Failed to fetch');
      },
    });
    expect(await screen.findByText('Failed to fetch')).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('keeps a signed-in session when a later probe fails, and says so', async () => {
    let healthy = true;
    function RefreshButton() {
      const auth = useAuth();
      return <button onClick={() => void auth.refresh()}>Probe</button>;
    }
    renderApp('/books', {
      'GET /auth/status': () => (healthy ? json(200, STATUS_OK) : problem(503, 'auth_unavailable', 'the credential store is unavailable')),
      'GET /me': () => me(),
    }, { library: <RefreshButton /> });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Probe' }));
    healthy = false;
    await user.click(screen.getByRole('button', { name: 'Probe' }));
    expect(await screen.findByText(/the credential store is unavailable/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Probe' })).toBeInTheDocument();
  });
});

describe('requests', () => {
  it('probes status once on mount and aborts it on unmount', async () => {
    let signal: AbortSignal | undefined;
    const { unmount, server } = renderApp('/books', {
      'GET /auth/status': (_c, req) => {
        signal = req.signal;
        return new Promise<Response>(() => {});
      },
    });
    await waitFor(() => expect(signal).toBeDefined());
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
    expect(server.count('GET /auth/status')).toBe(1);
  });
});

describe('usePermission and useAction', () => {
  const books = {
    id: 'books',
    permissionsHints: { view: [], actions: { organize: 'books:library.organize' } },
  } as unknown as ModuleUI;

  function Perms({ list }: { list: Permission[] }) {
    return (
      <ul>
        {list.map((p) => (
          <PermLine key={p} p={p} />
        ))}
        <ActionLine action="organize" />
        <ActionLine action="nope" />
      </ul>
    );
  }
  function PermLine({ p }: { p: Permission }) {
    return <li>{`${p}=${String(usePermission(p))}`}</li>;
  }
  function ActionLine({ action }: { action: string }) {
    return <li>{`action:${action}=${String(useAction('books', action))}`}</li>;
  }

  it('follows rbac.Covers and fails closed', async () => {
    renderApp(
      '/books',
      { 'GET /auth/status': () => json(200, STATUS_OK), 'GET /me': () => me(['core:users.view', 'books:library.*']) },
      {
        library: (
          <ModulesHintsContext value={[books]}>
            <Perms list={['core:users.view', 'core:users.manage', 'books:library.edit.metadata', 'books:edit.metadata', 'weird:']} />
          </ModulesHintsContext>
        ),
      },
    );
    expect(await screen.findByText('core:users.view=true')).toBeInTheDocument();
    expect(screen.getByText('core:users.manage=false')).toBeInTheDocument();
    expect(screen.getByText('books:library.edit.metadata=true')).toBeInTheDocument();
    expect(screen.getByText('books:edit.metadata=false')).toBeInTheDocument();
    expect(screen.getByText('weird:=false')).toBeInTheDocument();
    expect(screen.getByText('action:organize=true')).toBeInTheDocument();
    expect(screen.getByText('action:nope=false')).toBeInTheDocument();
  });

  it('wildcard "<ns>:*" covers the whole namespace', async () => {
    renderApp(
      '/books',
      { 'GET /auth/status': () => json(200, STATUS_OK), 'GET /me': () => me(['core:*']) },
      { library: <Perms list={['core:users.manage', 'books:library.view']} /> },
    );
    expect(await screen.findByText('core:users.manage=true')).toBeInTheDocument();
    expect(screen.getByText('books:library.view=false')).toBeInTheDocument();
    expect(screen.getByText('action:organize=false')).toBeInTheDocument();
  });
});

describe('usePermission outside a signed-in session', () => {
  function Line() {
    return <p>{`core:users.view=${String(usePermission('core:users.view'))}`}</p>;
  }

  it('is false, not a crash, with no provider at all', () => {
    render(<Line />);
    expect(screen.getByText('core:users.view=false')).toBeInTheDocument();
  });
});

describe('the shell operation table', () => {
  const client = createShellClient({ buildId: 'test', shell: true, fetch: fakeServer({}).fetch });

  it.each([
    ['get', '/auth/status'],
    ['post', '/auth/login'],
    ['post', '/auth/setup'],
    ['post', '/auth/bootstrap'],
    ['get', '/auth/providers'],
    ['post', '/auth/invites/lookup'],
    ['post', '/auth/invites/accept'],
    ['post', '/auth/temp-login/consume'],
  ])('%s %s is public: a 401 there never ends a session', (method, path) => {
    expect(client.runtime.skipAuthRedirect(method, `${API}${path}`)).toBe(true);
  });

  it.each([
    ['get', '/me'],
    ['post', '/auth/logout'],
    ['get', '/settings/schema'],
    ['get', '/settings'],
    ['patch', '/settings'],
  ])('%s %s is not public', (method, path) => {
    expect(client.runtime.skipAuthRedirect(method, `${API}${path}`)).toBe(false);
  });

  it('lists every route once', () => {
    const keys = shellOperations.map((o) => `${o.method} ${o.path}`);
    expect(new Set(keys).size).toBe(keys.length);
    expect(keys).toHaveLength(13);
  });
});
