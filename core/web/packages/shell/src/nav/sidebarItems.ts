// file: core/web/packages/shell/src/nav/sidebarItems.ts
// version: 0.1.0
// guid: dc685404-6112-44d3-be05-8a900bedac09
// last-edited: 2026-11-22

import CircleIcon from '@mui/icons-material/Circle';
import type { NavItem } from './navModel';
import type { SidebarItem } from '../layout/SidebarFrame';

/**
 * The sidebar's items for the resolved nav model: a group's children become
 * the frame's group children (its Library-style header and isSubItemSelected
 * rule), the entry's useBadge is handed through for the frame to call in a
 * component of its own, and matchPath defaults to the href's pathname so an
 * entry whose link carries a query still selects on its page. An entry with no
 * icon gets a plain dot, since the frame draws one for every row.
 */
export function toSidebarItems(items: readonly NavItem[]): SidebarItem[] {
  return items.map((it) => ({
    text: it.label,
    icon: it.entry.icon ?? CircleIcon,
    path: it.href,
    matchPath: it.matchPath ?? it.href.split('?')[0],
    ...(it.matchSearch !== undefined ? { matchSearch: it.matchSearch } : {}),
    ...(it.entry.useBadge ? { useBadge: it.entry.useBadge } : {}),
    ...(it.children.length > 0 ? { children: toSidebarItems(it.children) } : {}),
  }));
}
