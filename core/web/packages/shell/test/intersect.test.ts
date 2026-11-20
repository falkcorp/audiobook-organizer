// file: core/web/packages/shell/test/intersect.test.ts
// version: 0.1.0
// guid: a3f23f63-c2dd-4c68-8726-82e2d3a5c317
// last-edited: 2026-11-20

import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ModuleManifest, ModuleUI } from '../src/types';
import { intersect } from '../src/version/intersect';
import type { Diagnostic, ServerModule, ServerModules, ServerModuleUI } from '../src/version/types';

const lazy = async () => ({ default: () => null });

function manifest(id: string, version: string): ModuleManifest {
  return {
    id, version, displayName: id, pages: [], nav: [], settingsPanels: [], widgets: [], banners: [],
    topBarItems: [], userPage: [], activityPanes: [], setupSteps: [], permissions: [],
  };
}

/** A module contributing one item to every slot; nav has a child under `gadgets`. */
function fullModule(id = 'batcave', version = '1.2.0'): ModuleUI {
  return {
    id, version, manifest: manifest(id, version),
    routes: [{ id: 'gadgets', path: 'gadgets', lazy }, { id: 'cowl', path: 'cowl', lazy }],
    nav: [{ id: 'gadgets', label: 'Gadgets', to: 'gadgets', children: [{ id: 'utility-belt', label: 'Belt', to: 'cowl' }] }],
    settingsPanels: [{ id: 'alfred', label: 'Alfred' }],
    permissionsHints: { view: [] },
    widgets: [{ id: 'signal', placement: 'home', lazy }],
    banners: [{ id: 'alarm', lazy }],
    topBarItems: [{ id: 'bat-signal', lazy }],
    userPageExtensions: {
      tabs: [{ id: 'roster', label: 'Roster', lazy }],
      rowActions: [{ id: 'suspend', label: 'Suspend', lazy }],
    },
    activityPanes: [{ id: 'patrol', label: 'Patrol', lazy }],
    setup: { isRequired: async () => false, steps: [{ id: 'cave-root', title: 'Root', lazy }] },
  };
}

const FULL_UI: ServerModuleUI = {
  pages: ['gadgets', 'cowl'], nav: ['gadgets', 'utility-belt'], settings_panels: ['alfred'], widgets: ['signal'],
  banners: ['alarm'], top_bar_items: ['bat-signal'], user_page: ['roster', 'suspend'],
  activity_panes: ['patrol'], setup_steps: ['cave-root'],
};
const EMPTY_UI: ServerModuleUI = {
  pages: [], nav: [], settings_panels: [], widgets: [], banners: [], top_bar_items: [], user_page: [],
  activity_panes: [], setup_steps: [],
};

function serverModule(id: string, version: string, ui: ServerModuleUI = FULL_UI): ServerModule {
  return { id, version, display_name: id, ui };
}
function server(coreVersion: string, modules: ServerModule[]): ServerModules {
  return {
    core: { version: coreVersion },
    app: { id: 'abo', display_name: 'Wayne Tower', version: '2.0.0', ui_build_id: 'b3f1c2' },
    restart_required: false,
    modules,
  };
}
const kinds = (ds: Diagnostic[]) => ds.map((d) => d.kind);

describe('core version row', () => {
  it.each([
    ['0.4.0', '0.4.9', true],
    ['0.4.0', '0.5.0', false], // major 0: minors must be equal
    ['0.5.0', '0.4.0', false],
    ['0.4.0', '1.4.0', false],
    ['1.2.0', '1.3.0', true], // major >= 1: client minor <= server minor
    ['1.2.0', '1.2.7', true],
    ['1.3.0', '1.2.0', false],
    ['2.0.0', '1.9.0', false],
    ['1.0.0', '2.0.0', false],
    ['not-a-version', '1.0.0', false],
  ])('client %s against server %s -> mounts: %s', (client, srv, ok) => {
    const r = intersect(client, server(srv, [serverModule('batcave', '1.2.0')]), [fullModule()], { isAdmin: false });
    if (ok) {
      expect(r.blocked).toBeNull();
      expect(r.modules.map((m) => m.id)).toEqual(['batcave']);
    } else {
      expect(r.blocked).toEqual({ kind: 'core-version-incompatible', client, server: srv });
      expect(r.modules).toEqual([]);
      expect(kinds(r.diagnostics)).toEqual(['core-version-incompatible']);
    }
  });

  it('blocks before any module diagnostic is produced', () => {
    const r = intersect('0.4.0', server('0.5.0', [serverModule('arkham', '1.0.0')]), [], { isAdmin: true });
    expect(kinds(r.diagnostics)).toEqual(['core-version-incompatible']);
  });
});

describe('module rows', () => {
  it('no client entry and an empty server UI: nothing to render, no diagnostic', () => {
    const r = intersect('1.0.0', server('1.0.0', [serverModule('arkham', '1.0.0', EMPTY_UI)]), [], { isAdmin: true });
    expect(r.modules).toEqual([]);
    expect(r.diagnostics).toEqual([]);
  });

  it('no client entry and a non-empty server UI: ui-missing', () => {
    const ui = { ...EMPTY_UI, widgets: ['inmates'] };
    const r = intersect('1.0.0', server('1.0.0', [serverModule('arkham', '1.0.0', ui)]), [], { isAdmin: false });
    expect(r.modules).toEqual([]);
    expect(r.diagnostics).toMatchObject([{ kind: 'ui-missing', module: 'arkham' }]);
  });

  it.each([
    ['2.0.0', '1.2.0', false],
    ['1.2.0', '2.2.0', false],
    ['0.3.0', '0.4.0', false], // v0: minors must be equal
    ['0.4.0', '0.3.0', false],
    ['0.4.1', '0.4.9', true],
    ['1.3.0', '1.2.0', false], // client minor ahead
    ['1.2.0', '1.3.0', true],
    ['1.2.0', '1.2.0', true],
  ])('client %s against server %s -> mounted: %s', (clientV, serverV, ok) => {
    const r = intersect('1.0.0', server('1.0.0', [serverModule('batcave', serverV)]), [fullModule('batcave', clientV)], { isAdmin: false });
    expect(r.modules.length).toBe(ok ? 1 : 0);
    expect(kinds(r.diagnostics).includes('version-incompatible')).toBe(!ok);
    if (!ok) {
      expect(r.diagnostics).toHaveLength(1);
      expect(r.diagnostics[0]).toMatchObject({ kind: 'version-incompatible', module: 'batcave' });
    }
  });

  it('a registry module the server lacks is module-not-enabled, for admins only', () => {
    const srv = server('1.0.0', []);
    const admin = intersect('1.0.0', srv, [fullModule('arkham')], { isAdmin: true });
    expect(admin.modules).toEqual([]);
    expect(admin.diagnostics).toMatchObject([{ kind: 'module-not-enabled', module: 'arkham' }]);
    const user = intersect('1.0.0', srv, [fullModule('arkham')], { isAdmin: false });
    expect(user.modules).toEqual([]);
    expect(user.diagnostics).toEqual([]);
  });

  it('a server ID with no client item is ignored with a debug-level descriptor-without-ui', () => {
    const ui = { ...FULL_UI, pages: [...FULL_UI.pages, 'ghost'] };
    const r = intersect('1.0.0', server('1.0.0', [serverModule('batcave', '1.2.0', ui)]), [fullModule()], { isAdmin: true });
    expect(r.modules).toHaveLength(1);
    expect(r.diagnostics).toEqual([
      expect.objectContaining({ kind: 'descriptor-without-ui', module: 'batcave', item: 'pages:ghost', level: 'debug' }),
    ]);
  });

  it('a fully declared module passes through untouched with no diagnostics', () => {
    const c = fullModule();
    const r = intersect('1.0.0', server('1.0.0', [serverModule('batcave', '1.2.0')]), [c], { isAdmin: true });
    expect(r.diagnostics).toEqual([]);
    expect(r.modules[0]).toEqual(c);
  });
});

describe('undeclared-descriptor: every slot', () => {
  const ids = (xs: readonly { id: string }[] | undefined) => (xs ?? []).map((x) => x.id);
  const cases: { slot: keyof ServerModuleUI; drop: string; read: (m: ModuleUI) => string[] }[] = [
    { slot: 'pages', drop: 'cowl', read: (m) => ids(m.routes) },
    { slot: 'nav', drop: 'gadgets', read: (m) => ids(m.nav) },
    { slot: 'nav', drop: 'utility-belt', read: (m) => ids(m.nav[0]?.children) },
    { slot: 'settings_panels', drop: 'alfred', read: (m) => ids(m.settingsPanels) },
    { slot: 'widgets', drop: 'signal', read: (m) => ids(m.widgets) },
    { slot: 'banners', drop: 'alarm', read: (m) => ids(m.banners) },
    { slot: 'top_bar_items', drop: 'bat-signal', read: (m) => ids(m.topBarItems) },
    { slot: 'user_page', drop: 'roster', read: (m) => ids(m.userPageExtensions?.tabs) },
    { slot: 'user_page', drop: 'suspend', read: (m) => ids(m.userPageExtensions?.rowActions) },
    { slot: 'activity_panes', drop: 'patrol', read: (m) => ids(m.activityPanes) },
    { slot: 'setup_steps', drop: 'cave-root', read: (m) => ids(m.setup?.steps) },
  ];

  it.each(cases)('$slot:$drop is dropped when the server does not declare it', ({ slot, drop, read }) => {
    const ui = { ...FULL_UI, [slot]: FULL_UI[slot].filter((id) => id !== drop) };
    const before = read(fullModule());
    expect(before).toContain(drop);
    const r = intersect('1.0.0', server('1.0.0', [serverModule('batcave', '1.2.0', ui)]), [fullModule()], { isAdmin: false });
    expect(r.modules).toHaveLength(1);
    const after = read(r.modules[0]!);
    expect(after).toEqual(before.filter((id) => id !== drop));
    expect(r.diagnostics).toEqual([
      expect.objectContaining({ kind: 'undeclared-descriptor', module: 'batcave', item: `${slot}:${drop}` }),
    ]);
  });

  it('dropping a nav parent takes its children with it', () => {
    const ui = { ...FULL_UI, nav: [] };
    const r = intersect('1.0.0', server('1.0.0', [serverModule('batcave', '1.2.0', ui)]), [fullModule()], { isAdmin: false });
    expect(r.modules[0]!.nav).toEqual([]);
  });

  it('does not mutate the registry entry', () => {
    const c = fullModule();
    const ui = { ...EMPTY_UI };
    intersect('1.0.0', server('1.0.0', [serverModule('batcave', '1.2.0', ui)]), [c], { isAdmin: false });
    expect(c.routes).toHaveLength(2);
    expect(c.nav[0]!.children).toHaveLength(1);
  });
});

describe('reportDiagnostics', () => {
  afterEach(() => vi.resetModules());
  const load = async () => (await import('../src/version/diagnostics')).reportDiagnostics;
  const d = (kind: Diagnostic['kind'], module?: string, extra: Partial<Diagnostic> = {}): Diagnostic => ({
    kind, ...(module ? { module } : {}), detail: 'x', ...extra,
  });
  const okFetch = () => vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));

  it('posts each kind once per page load, across calls', async () => {
    const report = await load();
    const f = okFetch();
    await report(f, [d('ui-missing', 'arkham'), d('ui-missing', 'batcave'), d('version-incompatible', 'batcave')]);
    await report(f, [d('ui-missing', 'joker'), d('module-not-enabled', 'batcave')]);
    expect(f).toHaveBeenCalledTimes(3);
    const bodies = f.mock.calls.map(([, init]) => JSON.parse(String(init?.body)));
    expect(bodies).toEqual([
      { kind: 'ui-missing', module: 'arkham' },
      { kind: 'version-incompatible', module: 'batcave' },
      { kind: 'module-not-enabled', module: 'batcave' },
    ]);
    expect(f.mock.calls[0]![0]).toBe('/api/core/v1/ui/diagnostics');
    expect(f.mock.calls[0]![1]?.method).toBe('POST');
  });

  it('never reports debug-level entries', async () => {
    const report = await load();
    const f = okFetch();
    await report(f, [d('descriptor-without-ui', 'batcave', { level: 'debug' })]);
    expect(f).not.toHaveBeenCalled();
  });

  it('keeps every body within 4 KiB, truncating an oversized module label on a character boundary', async () => {
    const report = await load();
    const f = okFetch();
    await report(f, [d('ui-missing', 'bat\u{1F987}'.repeat(2000))]);
    const body = String(f.mock.calls[0]![1]?.body);
    expect(new TextEncoder().encode(body).length).toBeLessThanOrEqual(4096);
    const parsed = JSON.parse(body) as { module: string };
    expect(new TextEncoder().encode(parsed.module).length).toBeLessThanOrEqual(64);
    expect(parsed.module).not.toContain('�');
  });

  it('releases a kind whose POST failed so a later run retries it', async () => {
    const report = await load();
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const bad = vi.fn<typeof fetch>(async () => new Response(null, { status: 503 }));
    await report(bad, [d('ui-missing', 'arkham')]);
    const good = okFetch();
    await report(good, [d('ui-missing', 'arkham')]);
    expect(bad).toHaveBeenCalledTimes(1);
    expect(good).toHaveBeenCalledTimes(1);
    warn.mockRestore();
  });
});
