// file: core/web/packages/shell/src/nav/navModel.ts
// version: 0.1.0
// guid: 4b9e2d71-0a36-4c85-9f13-6d7a8e2c5b04
// last-edited: 2026-11-20

import type { ModuleManifest, ModuleUI, NavContribution } from '../types';
import type { Grants } from '../auth/hooks';
import { SHELL_GO_KEYS } from '../routes';

/** A nav entry resolved to a URL, with the module that contributed it. */
export interface NavItem {
  key: string; // `${moduleId}.${entry id}`
  moduleId: string;
  label: string;
  href: string;
  entry: NavContribution<ModuleManifest>;
  children: NavItem[];
}

/** The URL of a module page: /<module id>/<route path>, plus the entry's search. */
export function pageHref(mod: ModuleUI, to: NavContribution<ModuleManifest>['to']): string {
  const page = typeof to === 'string' ? to : to.page;
  const route = mod.routes.find((r) => r.id === page);
  if (!route) throw new Error(`nav: module ${mod.id} links to page ${page}, which it has no route for`);
  const search = typeof to === 'string' || !to.search ? '' : to.search.startsWith('?') ? to.search : `?${to.search}`;
  return `/${mod.id}/${route.path}${search}`;
}

/**
 * The sidebar model: each mounted module's nav entries in module ID order,
 * then by `order`, with an entry (and its children) hidden when the user
 * lacks its permission or its module's permissionsHints.view (any-of).
 */
export function buildNav(modules: readonly ModuleUI[], grants: Grants): NavItem[] {
  const out: NavItem[] = [];
  for (const mod of [...modules].sort((a, b) => a.id.localeCompare(b.id))) {
    const view = mod.permissionsHints.view;
    if (view.length > 0 && !view.some((p) => grants(p))) continue;
    const walk = (entries: readonly NavContribution<ModuleManifest>[]): NavItem[] =>
      [...entries]
        .filter((e) => grants(e.permission))
        .sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
        .map((e) => ({
          key: `${mod.id}.${e.id}`,
          moduleId: mod.id,
          label: e.label,
          href: pageHref(mod, e.to),
          entry: e,
          children: walk(e.children ?? []),
        }));
    out.push(...walk(mod.nav as readonly NavContribution<ModuleManifest>[]));
  }
  return out;
}

export interface GoEntry {
  key: string;
  href: string;
}

/**
 * The "g <key>" table (FE8-5): the shell's own pages first, then each nav
 * entry's goKey in module ID order. A key already taken is dropped with one
 * console warning, so no ui-kit hook names a module route.
 */
export function buildGoTable(items: readonly NavItem[], warn: (msg: string) => void = console.warn): GoEntry[] {
  const table: GoEntry[] = Object.entries(SHELL_GO_KEYS).map(([key, href]) => ({ key, href }));
  const taken = new Map(table.map((e) => [e.key, 'the shell']));
  const visit = (list: readonly NavItem[]) => {
    for (const it of list) {
      const k = it.entry.goKey;
      if (k !== undefined) {
        if (!/^[a-z]$/.test(k)) {
          warn(`nav ${it.key}: goKey ${JSON.stringify(k)} is not one lowercase letter; dropped`);
        } else if (taken.has(k)) {
          warn(`nav ${it.key}: goKey "${k}" is already used by ${taken.get(k)}; dropped`);
        } else {
          taken.set(k, it.key);
          table.push({ key: k, href: it.href });
        }
      }
      visit(it.children);
    }
  };
  visit(items);
  return table;
}
