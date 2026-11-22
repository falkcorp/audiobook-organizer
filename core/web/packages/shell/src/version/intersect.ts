// file: core/web/packages/shell/src/version/intersect.ts
// version: 0.1.0
// guid: cb15e419-85ef-4e23-9279-5bdc3c5010d3
// last-edited: 2026-11-20

// The server-manifest intersection (07 §7.5.2): which of the compiled-in
// modules, and which of their items, the server lets the shell mount.

import type { ModuleManifest, ModuleUI, NavContribution } from '../types';
import { isVersionCompatible } from './compare';
import type { Diagnostic, IntersectResult, ServerModules, ServerModuleUI } from './types';

type Slot = keyof ServerModuleUI;
const SLOTS: readonly Slot[] = [
  'pages',
  'nav',
  'settings_panels',
  'widgets',
  'banners',
  'top_bar_items',
  'user_page',
  'activity_panes',
  'setup_steps',
];

function navIds(items: readonly NavContribution<ModuleManifest>[]): string[] {
  const out: string[] = [];
  for (const n of items) {
    out.push(n.id);
    if (n.children) out.push(...navIds(n.children));
  }
  return out;
}

/** The IDs a client module carries in each server slot. */
function clientIds(c: ModuleUI): Record<Slot, string[]> {
  return {
    pages: c.routes.map((r) => r.id),
    nav: navIds(c.nav),
    settings_panels: c.settingsPanels.map((p) => p.id),
    widgets: (c.widgets ?? []).map((w) => w.id),
    banners: (c.banners ?? []).map((b) => b.id),
    top_bar_items: (c.topBarItems ?? []).map((t) => t.id),
    user_page: [
      ...(c.userPageExtensions?.tabs ?? []).map((t) => t.id),
      ...(c.userPageExtensions?.rowActions ?? []).map((r) => r.id),
      ...(c.userPageExtensions?.panels ?? []).map((p) => p.id),
    ],
    activity_panes: (c.activityPanes ?? []).map((a) => a.id),
    setup_steps: (c.setup?.steps ?? []).map((s) => s.id),
  };
}

function hasUI(ui: ServerModuleUI): boolean {
  return SLOTS.some((s) => (ui[s] ?? []).length > 0);
}

/** Drops every item whose ID the server does not declare, recording one diagnostic per drop. */
function dropUndeclared(c: ModuleUI, ui: ServerModuleUI, diagnostics: Diagnostic[]): ModuleUI {
  const declared: Record<Slot, Set<string>> = Object.fromEntries(
    SLOTS.map((s) => [s, new Set(ui[s] ?? [])]),
  ) as Record<Slot, Set<string>>;
  const keep = (slot: Slot, id: string): boolean => {
    if (declared[slot].has(id)) return true;
    diagnostics.push({
      kind: 'undeclared-descriptor',
      module: c.id,
      item: `${slot}:${id}`,
      detail: `module ${c.id} contributes ${slot} item "${id}" but the server does not declare it; dropped`,
    });
    return false;
  };
  // A dropped nav entry takes its children with it; a kept one filters its own.
  const filterNav = (items: readonly NavContribution<ModuleManifest>[]): NavContribution<ModuleManifest>[] =>
    items
      .filter((n) => keep('nav', n.id))
      .map((n) => (n.children ? { ...n, children: filterNav(n.children) } : n));

  const out: ModuleUI = {
    ...c,
    routes: c.routes.filter((r) => keep('pages', r.id)),
    nav: filterNav(c.nav),
    settingsPanels: c.settingsPanels.filter((p) => keep('settings_panels', p.id)),
  };
  if (c.widgets) out.widgets = c.widgets.filter((w) => keep('widgets', w.id));
  if (c.banners) out.banners = c.banners.filter((b) => keep('banners', b.id));
  if (c.topBarItems) out.topBarItems = c.topBarItems.filter((t) => keep('top_bar_items', t.id));
  if (c.userPageExtensions) {
    const ext = c.userPageExtensions;
    out.userPageExtensions = {
      ...(ext.tabs ? { tabs: ext.tabs.filter((t) => keep('user_page', t.id)) } : {}),
      ...(ext.rowActions ? { rowActions: ext.rowActions.filter((r) => keep('user_page', r.id)) } : {}),
      ...(ext.panels ? { panels: ext.panels.filter((p) => keep('user_page', p.id)) } : {}),
    };
  }
  if (c.activityPanes) out.activityPanes = c.activityPanes.filter((a) => keep('activity_panes', a.id));
  if (c.setup) out.setup = { ...c.setup, steps: c.setup.steps.filter((s) => keep('setup_steps', s.id)) };
  return out;
}

/**
 * Applies both tables of 07 §7.5.2. `core` is the shell's compile-time core
 * version (`C.core`). A core mismatch blocks everything; otherwise each server
 * module is paired with its registry entry. Modules come back in server order.
 */
export function intersect(
  core: string,
  server: ServerModules,
  registry: readonly ModuleUI[],
  opts: { isAdmin: boolean },
): IntersectResult {
  const diagnostics: Diagnostic[] = [];

  if (!isVersionCompatible(core, server.core.version)) {
    diagnostics.push({
      kind: 'core-version-incompatible',
      detail: `shell core ${core} is not compatible with server core ${server.core.version}`,
    });
    return {
      blocked: { kind: 'core-version-incompatible', client: core, server: server.core.version },
      modules: [],
      diagnostics,
    };
  }

  const byId = new Map(registry.map((m) => [m.id, m]));
  const serverIds = new Set(server.modules.map((m) => m.id));
  const modules: ModuleUI[] = [];

  for (const s of server.modules) {
    const c = byId.get(s.id);
    if (!c) {
      if (hasUI(s.ui)) {
        diagnostics.push({
          kind: 'ui-missing',
          module: s.id,
          detail: `the server declares UI for module ${s.id} but this bundle does not contain it`,
        });
      }
      continue;
    }
    if (!isVersionCompatible(c.version, s.version)) {
      diagnostics.push({
        kind: 'version-incompatible',
        module: s.id,
        detail: `module ${s.id} client ${c.version} is not compatible with server ${s.version}; not mounted`,
      });
      continue;
    }
    modules.push(dropUndeclared(c, s.ui, diagnostics));

    const have = clientIds(c);
    for (const slot of SLOTS) {
      const known = new Set(have[slot]);
      for (const id of s.ui[slot] ?? []) {
        if (known.has(id)) continue;
        diagnostics.push({
          kind: 'descriptor-without-ui',
          module: s.id,
          item: `${slot}:${id}`,
          detail: `the server declares ${slot} item "${id}" for module ${s.id} but the client has none`,
          level: 'debug',
        });
      }
    }
  }

  if (opts.isAdmin) {
    for (const c of registry) {
      if (serverIds.has(c.id)) continue;
      diagnostics.push({
        kind: 'module-not-enabled',
        module: c.id,
        detail: `module ${c.id} is in this bundle but the server has it disabled or does not know it; not mounted`,
      });
    }
  }

  return { blocked: null, modules, diagnostics };
}
