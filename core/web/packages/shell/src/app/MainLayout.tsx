// file: core/web/packages/shell/src/app/MainLayout.tsx
// version: 0.1.0
// guid: 1e7c4b93-6a20-4d58-9f31-b5e8d2a0c674
// last-edited: 2026-11-20

import { lazy, Suspense, useMemo, useState, type ComponentType } from 'react';
import {
  AppBar,
  Box,
  Divider,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemText,
  Menu,
  MenuItem,
  Toolbar,
  Tooltip,
  Typography,
} from '@mui/material';
import { useColorScheme } from '@mui/material/styles';
import DarkModeIcon from '@mui/icons-material/DarkMode';
import LightModeIcon from '@mui/icons-material/LightMode';
import AccountCircleIcon from '@mui/icons-material/AccountCircle';
import { Link as RouterLink, Outlet, useLocation, useNavigate } from 'react-router';
import { useKeyboardShortcuts } from '@mediacore/ui-kit';
import { useAuth, useGrants } from '../auth';
import { buildGoTable, buildNav } from '../nav/navModel';
import { ShellNav } from '../nav/ShellNav';
import type { Lazy, Permission } from '../types';
import { useMountedModules, useShellContext } from './context';
import { ShellErrorBoundary } from './ShellErrorBoundary';

const DRAWER = 240;

/** The shell's own sidebar entries; Users and System need core:admin. */
const SHELL_ENTRIES: readonly { label: string; to: string; permission?: Permission }[] = [
  { label: 'Home', to: '/' },
  { label: 'Activity', to: '/activity' },
  { label: 'Settings', to: '/settings' },
  { label: 'System', to: '/system', permission: 'core:admin' },
  { label: 'Users', to: '/users', permission: 'core:admin' },
];

const lazyCache = new WeakMap<Lazy, ComponentType>();
/** One React.lazy per contribution, so a re-render never remounts it. */
export function lazyOf(load: Lazy): ComponentType {
  let c = lazyCache.get(load);
  if (!c) {
    c = lazy(load);
    lazyCache.set(load, c);
  }
  return c;
}

/** A contribution's component, loaded on demand, inside its own boundary. */
export function LazySlot({ load, label }: { load: Lazy; label: string }) {
  const C = lazyOf(load);
  return (
    <ShellErrorBoundary label={label}>
      <Suspense fallback={null}>
        <C />
      </Suspense>
    </ShellErrorBoundary>
  );
}

function ThemeToggle() {
  const { mode, setMode } = useColorScheme();
  const dark = mode !== 'light';
  return (
    <Tooltip title="Toggle color mode">
      <IconButton color="inherit" aria-label="toggle color mode" onClick={() => setMode(dark ? 'light' : 'dark')}>
        {dark ? <LightModeIcon /> : <DarkModeIcon />}
      </IconButton>
    </Tooltip>
  );
}

function UserMenu() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  return (
    <>
      <IconButton color="inherit" aria-label="user menu" onClick={(e) => setAnchor(e.currentTarget)}>
        <AccountCircleIcon />
      </IconButton>
      <Menu anchorEl={anchor} open={anchor !== null} onClose={() => setAnchor(null)}>
        <MenuItem disabled>{auth.user?.display_name || auth.user?.username}</MenuItem>
        <MenuItem
          onClick={async () => {
            setAnchor(null);
            await auth.logout();
            navigate('/login');
          }}
        >
          Sign out
        </MenuItem>
      </Menu>
    </>
  );
}

/**
 * The signed-in frame (07 §7.4.2): top bar with module topBarItems, the
 * sidebar (shell entries, then module nav), module banners above the page, and the "g <key>" shortcuts.
 */
export function MainLayout() {
  const { displayName } = useShellContext();
  const { modules } = useMountedModules();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const grants = useGrants();
  const items = useMemo(() => buildNav(modules, grants), [modules, grants]);
  const goTable = useMemo(() => buildGoTable(items).map((e) => ({ key: e.key, run: () => navigate(e.href) })), [items, navigate]);
  useKeyboardShortcuts(goTable);

  type Slot = { id: string; lazy: Lazy; permission?: string; order?: number; moduleId: string };
  const collect = (pick: (m: (typeof modules)[number]) => readonly { id: string; lazy: Lazy; permission?: string; order?: number }[] | undefined): Slot[] =>
    modules
      .flatMap((m) => (pick(m) ?? []).map((s) => ({ ...s, moduleId: m.id })))
      .filter((s) => grants(s.permission))
      .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  const banners = collect((m) => m.banners);
  const topBar = collect((m) => m.topBarItems);

  return (
    <Box sx={{ display: 'flex', minHeight: '100vh' }}>
      <AppBar position="fixed" sx={{ zIndex: (t) => t.zIndex.drawer + 1 }}>
        <Toolbar>
          <Typography variant="h6" component={RouterLink} to="/" sx={{ flexGrow: 1, color: 'inherit', textDecoration: 'none' }}>
            {displayName}
          </Typography>
          {topBar.map((t) => (
            <LazySlot key={`${t.moduleId}.${t.id}`} load={t.lazy} label={`${t.moduleId} ${t.id}`} />
          ))}
          <ThemeToggle />
          <UserMenu />
        </Toolbar>
      </AppBar>
      <Drawer variant="permanent" sx={{ width: DRAWER, flexShrink: 0, '& .MuiDrawer-paper': { width: DRAWER, boxSizing: 'border-box' } }}>
        <Toolbar />
        <List component="nav" aria-label="Shell">
          {SHELL_ENTRIES.filter((e) => grants(e.permission)).map((e) => (
            <ListItemButton key={e.to} component={RouterLink} to={e.to} selected={e.to === '/' ? pathname === '/' : pathname.startsWith(e.to)}>
              <ListItemText primary={e.label} />
            </ListItemButton>
          ))}
        </List>
        <Divider />
        <ShellNav items={items} />
      </Drawer>
      <Box component="main" sx={{ flexGrow: 1, minWidth: 0 }}>
        <Toolbar />
        {banners.map((b) => (
          <LazySlot key={`${b.moduleId}.${b.id}`} load={b.lazy} label={`${b.moduleId} banner ${b.id}`} />
        ))}
        <ShellErrorBoundary label="This page">
          <Outlet />
        </ShellErrorBoundary>
      </Box>
    </Box>
  );
}

