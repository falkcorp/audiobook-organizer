// file: core/web/packages/shell/src/defineModule.ts
// version: 0.1.0
// guid: 3a7e1c95-6d20-4f48-b9c3-2e8f0a5d7b16
// last-edited: 2026-11-20

import type { ModuleManifest, ModuleUI, ModuleUIInput, NavContribution } from './types';

const ROUTE_PATH = /^(?!\/)[A-Za-z0-9_\-:/*.]*$/;

/**
 * Binds a module's UI contributions to its generated manifest (07 §7.4.2).
 * The types key every slot by the manifest's literal IDs, so an undeclared ID
 * is a compile error; this function adds the checks a type cannot make:
 * a duplicate ID within a slot and a route path that is not relative.
 */
export function defineModule<const M extends ModuleManifest>(manifest: M, ui: ModuleUIInput<M>): ModuleUI<M> {
  const dup = (slot: string, ids: readonly string[]) => {
    const seen = new Set<string>();
    for (const id of ids) {
      if (seen.has(id)) throw new Error(`defineModule(${manifest.id}): ${slot} lists ${JSON.stringify(id)} twice`);
      seen.add(id);
    }
  };
  dup('routes', ui.routes.map((r) => r.id));
  dup('settingsPanels', ui.settingsPanels.map((p) => p.id));
  dup('widgets', (ui.widgets ?? []).map((w) => w.id));
  dup('banners', (ui.banners ?? []).map((b) => b.id));
  dup('topBarItems', (ui.topBarItems ?? []).map((t) => t.id));
  dup('activityPanes', (ui.activityPanes ?? []).map((a) => a.id));
  for (const r of ui.routes) {
    if (!ROUTE_PATH.test(r.path)) throw new Error(`defineModule(${manifest.id}): route ${r.id} path ${JSON.stringify(r.path)} must be relative to /${manifest.id}/`);
  }
  const checkNav = (entries: readonly NavContribution<M>[]) => {
    for (const e of entries) {
      if (e.matchPath !== undefined && !ROUTE_PATH.test(e.matchPath)) {
        throw new Error(`defineModule(${manifest.id}): nav ${e.id} matchPath ${JSON.stringify(e.matchPath)} must be relative to /${manifest.id}/`);
      }
      checkNav(e.children ?? []);
    }
  };
  checkNav(ui.nav);
  for (const key of Object.keys(ui.taskKinds ?? {})) {
    if (!key.startsWith(`${manifest.id}.`)) throw new Error(`defineModule(${manifest.id}): task kind ${key} is outside the module's namespace`);
  }
  return { ...ui, id: manifest.id, version: manifest.version, manifest };
}
