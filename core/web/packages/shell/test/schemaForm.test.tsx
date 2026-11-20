// file: core/web/packages/shell/test/schemaForm.test.tsx
// version: 0.1.0
// guid: 977778a4-f55e-404d-9aa4-00493fc55fd4
// last-edited: 2026-11-20

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createShellClient, type KeyStateView, type SettingsPatchResult, type SettingsViewBody } from '../src/api/interim';
import { SchemaForm, SettingsSection } from '../src/settings';

const API = '/api/core/v1';
const SECTION = 'modules.batcave';

interface Call {
  method: string;
  path: string;
  body: unknown;
}
type Handler = (call: Call, req: Request) => Response | Promise<Response>;

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function problem(status: number, code: string, detail: string, extra: Record<string, unknown> = {}): Response {
  const body = { type: `https://mediacore.dev/problems/${code}`, title: 'Problem', status, code, detail, request_id: 'req-1', ...extra };
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/problem+json' } });
}

function server(routes: Record<string, Handler>) {
  const calls: Call[] = [];
  const fetchFn: typeof fetch = async (input, init) => {
    const req = input instanceof Request ? input : new Request(input, init);
    const url = new URL(req.url);
    const text = await req.clone().text();
    const call: Call = { method: req.method, path: url.pathname, body: text ? JSON.parse(text) : undefined };
    calls.push(call);
    const handler = routes[`${req.method} ${url.pathname.replace(API, '')}`];
    if (!handler) throw new Error(`unexpected request ${req.method} ${url.pathname}`);
    return handler(call, req);
  };
  return { calls, client: createShellClient({ buildId: 'test', shell: true, fetch: fetchFn }), count: (key: string) => calls.filter((c) => `${c.method} ${c.path.replace(API, '')}` === key).length };
}

function key(name: string, schema: KeyStateView['schema'], over: Partial<KeyStateView> = {}): KeyStateView {
  return { path: `${SECTION}.${name}`, section: SECTION, schema, effective: undefined, source: 'default', reload: 'live', protection: 'none', ...over };
}

const KEYS: KeyStateView[] = [
  key('codename', { type: 'string', title: 'Codename', description: 'What the cave is called.' }, { effective: 'batcave' }),
  key('alarm_level', { type: 'integer', title: 'Alarm level', minimum: 0, maximum: 5 }, { effective: 2, source: 'override' }),
  key('lights_on', { type: 'boolean', title: 'Lights on' }, { effective: true }),
  key('vehicle', { type: 'string', title: 'Vehicle', enum: ['batmobile', 'batwing', 'batboat'] }, { effective: 'batmobile' }),
  key('uplink_key', { type: 'string', title: 'Uplink key', writeOnly: true }, { secret: { set: true, source: 'stored' } }),
  key('siren_pitch', { type: 'string', title: 'Siren pitch', format: 'duration' }, { effective: '5s' }),
  key('allies', { type: 'array', title: 'Allies', items: { type: 'string' } }, { effective: ['alfred'] }),
  key('beacon_url', { type: 'string', title: 'Beacon URL', format: 'uri' }, { effective: 'https://beacon.example.com', locked_by: 'env ABO_MODULES_BATCAVE_BEACON_URL' }),
  key('listen_port', { type: 'integer', title: 'Listen port' }, { effective: 8080, reload: 'restart', pending: true }),
];

afterEach(() => vi.restoreAllMocks());

function setup(keys: KeyStateView[] = KEYS, patch?: Handler, onSaved?: (r: SettingsPatchResult) => void) {
  const s = server({ 'PATCH /settings': patch ?? (() => json(200, { generation: 2, applied: [] })) });
  render(<SchemaForm client={s.client} section={SECTION} schema={{ title: 'Batcave' }} keys={keys} onSaved={onSaved} />);
  return s;
}

describe('widgets', () => {
  it('renders one widget per JSON Schema type', () => {
    setup();
    expect(screen.getByRole('heading', { name: 'Batcave' })).toBeInTheDocument();
    // string -> text field
    expect(screen.getByRole('textbox', { name: 'Codename' })).toHaveValue('batcave');
    expect(screen.getByText('What the cave is called.')).toBeInTheDocument();
    // integer -> number field
    expect(screen.getByRole('spinbutton', { name: 'Alarm level' })).toHaveValue(2);
    // boolean -> switch
    expect(screen.getByRole('switch', { name: 'Lights on' })).toBeChecked();
    // enum -> select
    expect(screen.getByRole('combobox', { name: 'Vehicle' })).toHaveTextContent('batmobile');
    // array of strings -> chip list
    expect(screen.getByText('alfred')).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: 'Allies' })).toBeInTheDocument();
  });

  it('shows a secret as set or not set, never its value, as a password field', () => {
    setup([
      ...KEYS,
      key('spare_key', { type: 'string', title: 'Spare key', writeOnly: true }, { secret: { set: false } }),
    ]);
    const field = screen.getByLabelText('Uplink key');
    expect(field).toHaveAttribute('type', 'password');
    expect(field).toHaveValue('');
    expect(screen.getByText(/Set \(stored\)\. Write-only/)).toBeInTheDocument();
    expect(screen.getByLabelText('Spare key')).toHaveAttribute('type', 'password');
    expect(screen.getByText(/Not set\. Write-only/)).toBeInTheDocument();
  });

  it('disables a locked key and shows who holds the lock', () => {
    setup();
    const field = screen.getByRole('textbox', { name: 'Beacon URL' });
    expect(field).toBeDisabled();
    expect(screen.getByText('Locked by env ABO_MODULES_BATCAVE_BEACON_URL. Change it there.')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Codename' })).toBeEnabled();
  });

  it('flags a restart-required pending change and a key that needs a restart', () => {
    setup();
    expect(screen.getByText('Restart required')).toBeInTheDocument();
    expect(screen.queryByText('Applies after a restart.')).not.toBeInTheDocument();
  });

  it('says "applies after a restart" for a restart-reload key with nothing pending', () => {
    setup([key('listen_port', { type: 'integer', title: 'Listen port' }, { effective: 8080, reload: 'restart' })]);
    expect(screen.getByText('Applies after a restart.')).toBeInTheDocument();
  });

  it('shows an empty state when the section has no keys', () => {
    setup([key('x', { type: 'string' })].map((k) => ({ ...k, section: 'modules.other' })));
    expect(screen.getByText('This section has no settings.')).toBeInTheDocument();
  });

  it('blocks a value that fails its format or range and says why', async () => {
    setup();
    const user = userEvent.setup();
    await user.clear(screen.getByRole('textbox', { name: 'Siren pitch' }));
    await user.type(screen.getByRole('textbox', { name: 'Siren pitch' }), 'soon');
    expect(screen.getByText('Not a valid duration.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();

    await user.clear(screen.getByRole('textbox', { name: 'Siren pitch' }));
    await user.type(screen.getByRole('textbox', { name: 'Siren pitch' }), '10s');
    await user.clear(screen.getByRole('spinbutton', { name: 'Alarm level' }));
    await user.type(screen.getByRole('spinbutton', { name: 'Alarm level' }), '9');
    expect(screen.getByText('Must be at most 5.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();

    await user.clear(screen.getByRole('spinbutton', { name: 'Alarm level' }));
    await user.type(screen.getByRole('spinbutton', { name: 'Alarm level' }), '-1');
    expect(screen.getByText('Must be at least 0.')).toBeInTheDocument();

    await user.clear(screen.getByRole('spinbutton', { name: 'Alarm level' }));
    await user.type(screen.getByRole('spinbutton', { name: 'Alarm level' }), '2.5');
    expect(screen.getByText('Enter a whole number.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('groups keys under their x-mediacore-ui group, in order', () => {
    setup([
      key('b_key', { type: 'string', title: 'First', 'x-mediacore-ui': { group: 'Alarms', order: 1 } }, { effective: '' }),
      key('a_key', { type: 'string', title: 'Second', 'x-mediacore-ui': { group: 'Alarms', order: 2 } }, { effective: '' }),
      key('c_key', { type: 'string', title: 'Third', 'x-mediacore-ui': { group: 'Gadgets' } }, { effective: '' }),
    ]);
    expect(screen.getByRole('heading', { name: 'Alarms' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Gadgets' })).toBeInTheDocument();
    expect(screen.getAllByRole('textbox').map((el) => (el as HTMLInputElement).labels?.[0]?.textContent)).toEqual(['First', 'Second', 'Third']);
  });
});

describe('saving', () => {
  it('sends only the keys that changed', async () => {
    const onSaved = vi.fn();
    const s = setup(KEYS, () => json(200, { generation: 3, applied: [`${SECTION}.codename`, `${SECTION}.lights_on`, `${SECTION}.alarm_level`, `${SECTION}.uplink_key`] }), onSaved);
    const user = userEvent.setup();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();

    await user.clear(screen.getByRole('textbox', { name: 'Codename' }));
    await user.type(screen.getByRole('textbox', { name: 'Codename' }), 'wayne manor');
    await user.click(screen.getByRole('switch', { name: 'Lights on' }));
    await user.clear(screen.getByRole('spinbutton', { name: 'Alarm level' }));
    await user.type(screen.getByRole('spinbutton', { name: 'Alarm level' }), '4');
    await user.type(screen.getByLabelText('Uplink key'), 'grapple-77');
    // Typing a value back to what it was is not a change.
    await user.clear(screen.getByRole('textbox', { name: 'Siren pitch' }));
    await user.type(screen.getByRole('textbox', { name: 'Siren pitch' }), '5s');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByText('Saved 4 settings.')).toBeInTheDocument();
    const patch = s.calls.find((c) => c.method === 'PATCH');
    expect(patch?.body).toEqual({
      set: {
        [`${SECTION}.codename`]: 'wayne manor',
        [`${SECTION}.lights_on`]: false,
        [`${SECTION}.alarm_level`]: 4,
        [`${SECTION}.uplink_key`]: 'grapple-77',
      },
    });
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('sends a chip list and an enum choice as typed values', async () => {
    const s = setup();
    const user = userEvent.setup();
    await user.type(screen.getByRole('combobox', { name: 'Allies' }), 'robin{Enter}');
    await user.click(screen.getByRole('combobox', { name: 'Vehicle' }));
    await user.click(await screen.findByRole('option', { name: 'batwing' }));
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(s.calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(s.calls.find((c) => c.method === 'PATCH')?.body).toEqual({
      set: { [`${SECTION}.allies`]: ['alfred', 'robin'], [`${SECTION}.vehicle`]: 'batwing' },
    });
  });

  it('puts a reset key in unset, and a cleared secret in unset', async () => {
    const s = setup();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Reset Alarm level to default' }));
    expect(screen.getByText('Will reset to default')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Clear Uplink key' }));
    expect(screen.getByText('Will be cleared')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(s.calls.some((c) => c.method === 'PATCH')).toBe(true));
    expect(s.calls.find((c) => c.method === 'PATCH')?.body).toEqual({ unset: [`${SECTION}.alarm_level`, `${SECTION}.uplink_key`] });
  });

  it('offers no reset for a default value, a locked key, or a secret that is not stored', () => {
    setup([
      ...KEYS,
      key('gate_code', { type: 'string', title: 'Gate code' }, { effective: '1939', source: 'override', locked_by: 'flag modules.batcave.gate_code' }),
      key('env_key', { type: 'string', title: 'Env key', writeOnly: true }, { secret: { set: true, source: 'env' } }),
    ]);
    expect(screen.getByRole('button', { name: 'Reset Alarm level to default' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reset Codename to default' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reset Beacon URL to default' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reset Gate code to default' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Clear Uplink key' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Clear Env key' })).not.toBeInTheDocument();
  });

  it('aborts a save in flight on unmount', async () => {
    let signal: AbortSignal | undefined;
    const s = server({
      'PATCH /settings': (_c, req) => {
        signal = req.signal;
        return new Promise<Response>(() => {});
      },
    });
    const { unmount } = render(<SchemaForm client={s.client} section={SECTION} keys={KEYS} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('switch', { name: 'Lights on' }));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(signal).toBeDefined());
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('discarding drops every staged change', async () => {
    const s = setup();
    const user = userEvent.setup();
    await user.click(screen.getByRole('switch', { name: 'Lights on' }));
    await user.click(screen.getByRole('button', { name: 'Reset Alarm level to default' }));
    await user.click(screen.getByRole('button', { name: 'Discard changes' }));
    expect(screen.getByRole('switch', { name: 'Lights on' })).toBeChecked();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    expect(s.calls).toHaveLength(0);
  });

  it('shows what the server says needs a restart, and a failed after-commit hook', async () => {
    setup(KEYS, () =>
      json(200, {
        generation: 4,
        applied: [`${SECTION}.listen_port`],
        restart_pending: [`${SECTION}.listen_port`],
        post_commit_error: [{ section: SECTION, message: 'the siren would not start' }],
      }),
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole('switch', { name: 'Lights on' }));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(`A restart is needed before these take effect: ${SECTION}.listen_port.`)).toBeInTheDocument();
    expect(screen.getByText(`${SECTION}: saved, but applying it failed: the siren would not start`)).toBeInTheDocument();
  });

  it('shows a 422 validation problem, in the alert and under the key it names', async () => {
    setup(KEYS, () =>
      problem(422, 'validation_failed', 'one or more settings are not valid', {
        errors: [{ code: 'range', field: `${SECTION}.alarm_level`, message: 'must be a level the cave can sound' }],
      }),
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole('switch', { name: 'Lights on' }));
    await user.click(screen.getByRole('button', { name: 'Save' }));

    const alert = await screen.findByRole('alert');
    expect(within(alert).getByText('one or more settings are not valid')).toBeInTheDocument();
    expect(within(alert).getByText(`${SECTION}.alarm_level: must be a level the cave can sound`)).toBeInTheDocument();
    expect(screen.getAllByText('must be a level the cave can sound').length).toBeGreaterThan(0);
    // The staged change survives so it can be corrected.
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
  });

  it('shows a 409 locked refusal with the layer that holds the key', async () => {
    setup(KEYS, () =>
      problem(409, 'locked', 'a key is supplied by an operator layer', { locked_by: { [`${SECTION}.codename`]: 'env ABO_MODULES_BATCAVE_CODENAME' } }),
    );
    const user = userEvent.setup();
    await user.type(screen.getByRole('textbox', { name: 'Codename' }), '!');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(`${SECTION}.codename is locked by env ABO_MODULES_BATCAVE_CODENAME`)).toBeInTheDocument();
  });

  it('shows a 403 refusal and a network failure', async () => {
    let n = 0;
    setup(KEYS, () => {
      n += 1;
      if (n === 1) return problem(403, 'permission_denied', 'you may not change this', { need: { [`${SECTION}.codename`]: 'modules.batcave:settings.manage' } });
      throw new TypeError('Failed to fetch');
    });
    const user = userEvent.setup();
    await user.type(screen.getByRole('textbox', { name: 'Codename' }), '!');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(`${SECTION}.codename needs modules.batcave:settings.manage`)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Failed to fetch')).toBeInTheDocument();
  });
});

describe('SettingsSection', () => {
  const view = (): SettingsViewBody => Object.fromEntries(KEYS.map((k) => [k.path, k]));
  const routes = (over: Record<string, Handler> = {}): Record<string, Handler> => ({
    'GET /settings/schema': () => json(200, { type: 'object', properties: { [SECTION]: { type: 'object', title: 'Batcave settings', description: 'Everything about the cave.' } } }),
    'GET /settings': () => json(200, view()),
    ...over,
  });

  it('loads the schema and the values once, then renders the form', async () => {
    const s = server(routes());
    render(<SettingsSection client={s.client} section={SECTION} />);
    expect(screen.getByRole('status')).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'Batcave settings' })).toBeInTheDocument();
    expect(screen.getByText('Everything about the cave.')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Codename' })).toHaveValue('batcave');
    expect(s.count('GET /settings/schema')).toBe(1);
    expect(s.count('GET /settings')).toBe(1);
  });

  it('shows the server error with a retry that loads', async () => {
    let healthy = false;
    const s = server(routes({ 'GET /settings': () => (healthy ? json(200, view()) : problem(500, 'internal', 'the cave computer is down')) }));
    render(<SettingsSection client={s.client} section={SECTION} />);
    expect(await screen.findByText('the cave computer is down')).toBeInTheDocument();
    healthy = true;
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('textbox', { name: 'Codename' })).toBeInTheDocument();
  });

  it('shows an empty state for a section with no keys', async () => {
    const s = server(routes());
    render(<SettingsSection client={s.client} section="modules.empty" />);
    expect(await screen.findByText('This section has no settings.')).toBeInTheDocument();
  });

  it('aborts both requests on unmount', async () => {
    const signals: AbortSignal[] = [];
    const never: Handler = (_c, req) => {
      signals.push(req.signal);
      return new Promise<Response>(() => {});
    };
    const s = server(routes({ 'GET /settings/schema': never, 'GET /settings': never }));
    const { unmount } = render(<SettingsSection client={s.client} section={SECTION} />);
    await waitFor(() => expect(signals).toHaveLength(2));
    expect(signals.every((x) => !x.aborted)).toBe(true);
    unmount();
    expect(signals.every((x) => x.aborted)).toBe(true);
  });

  it('refetches the values after a save and keeps the form up', async () => {
    let codename = 'batcave';
    const s = server(
      routes({
        'GET /settings': () => json(200, Object.fromEntries(KEYS.map((k) => [k.path, k.path.endsWith('.codename') ? { ...k, effective: codename } : k]))),
        'PATCH /settings': (c) => {
          codename = (c.body as { set: Record<string, string> }).set[`${SECTION}.codename`];
          return json(200, { generation: 2, applied: [`${SECTION}.codename`] });
        },
      }),
    );
    render(<SettingsSection client={s.client} section={SECTION} />);
    const user = userEvent.setup();
    const field = await screen.findByRole('textbox', { name: 'Codename' });
    await user.clear(field);
    await user.type(field, 'wayne manor');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(s.count('GET /settings')).toBe(2));
    expect(await screen.findByText('Saved 1 setting.')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Codename' })).toHaveValue('wayne manor');
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });
});
