// file: core/web/packages/shell/src/nav/ShellNav.tsx
// version: 0.1.0
// guid: 9d3c7e50-2f18-4b64-a0e9-3c5b1f8d6a27
// last-edited: 2026-11-20

import { Badge, List, ListItemButton, ListItemIcon, ListItemText } from '@mui/material';
import type { ReactNode } from 'react';
import { Link as RouterLink, useLocation } from 'react-router';
import type { NavItem } from './navModel';

/**
 * Calls one entry's useBadge (FE-22). A component per entry, so the hook is
 * called unconditionally at the top of a component, as the rules of hooks
 * and the React Compiler require; an entry without useBadge never renders
 * this component.
 */
export function NavBadge({ useBadge, children }: { useBadge: () => number | undefined; children: ReactNode }) {
  const count = useBadge();
  return (
    <Badge color="secondary" badgeContent={count} invisible={!count} data-testid="nav-badge">
      {children}
    </Badge>
  );
}

function Entry({ item, depth }: { item: NavItem; depth: number }) {
  const { pathname } = useLocation();
  const Icon = item.entry.icon;
  const icon = Icon ? <Icon fontSize="small" /> : null;
  const useBadge = item.entry.useBadge;
  return (
    <>
      <ListItemButton component={RouterLink} to={item.href} selected={pathname === item.href.split('?')[0]} sx={{ pl: 2 + depth * 2 }}>
        {icon && <ListItemIcon>{useBadge ? <NavBadge useBadge={useBadge}>{icon}</NavBadge> : icon}</ListItemIcon>}
        <ListItemText primary={!icon && useBadge ? <NavBadge useBadge={useBadge}>{item.label}</NavBadge> : item.label} />
      </ListItemButton>
      {item.children.map((c) => (
        <Entry key={c.key} item={c} depth={depth + 1} />
      ))}
    </>
  );
}

/** The sidebar list of module entries (shell entries are added by the layout). */
export function ShellNav({ items }: { items: readonly NavItem[] }) {
  return (
    <List component="nav" aria-label="Modules">
      {items.map((it) => (
        <Entry key={it.key} item={it} depth={0} />
      ))}
    </List>
  );
}
