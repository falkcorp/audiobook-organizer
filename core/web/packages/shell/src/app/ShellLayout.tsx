// file: core/web/packages/shell/src/app/ShellLayout.tsx
// version: 0.1.0
// guid: 863143f0-3a92-4e7c-a207-58f7644bf5e0
// last-edited: 2026-11-22

import { useMemo } from 'react';
import { Box } from '@mui/material';
import HomeIcon from '@mui/icons-material/Home';
import MonitorIcon from '@mui/icons-material/Monitor';
import PeopleIcon from '@mui/icons-material/People';
import SettingsIcon from '@mui/icons-material/Settings';
import TimelineIcon from '@mui/icons-material/Timeline';
import { Outlet, useNavigate } from 'react-router';
import { useKeyboardShortcuts } from '@mediacore/ui-kit';
import { useGrants } from '../auth';
import { MainLayoutFrame } from '../layout/MainLayoutFrame';
import type { SidebarItem } from '../layout/SidebarFrame';
import { TopBarFrame } from '../layout/TopBarFrame';
import { UserMenu } from '../layout/UserMenu';
import { buildGoTable, buildNav } from '../nav/navModel';
import { toSidebarItems } from '../nav/sidebarItems';
import type { Lazy, Permission } from '../types';
import { useMountedModules, useShellContext } from './context';
import { LazySlot } from './LazySlot';
import { ShellErrorBoundary } from './ShellErrorBoundary';

/** The shell's own sidebar entries; Users and System need core:admin. */
const SHELL_ENTRIES: readonly { item: SidebarItem; permission?: Permission }[] = [
  { item: { text: 'Home', icon: HomeIcon, path: '/' } },
  { item: { text: 'Activity', icon: TimelineIcon, path: '/activity', matchPrefix: true } },
  { item: { text: 'Settings', icon: SettingsIcon, path: '/settings', matchPrefix: true } },
  { item: { text: 'System', icon: MonitorIcon, path: '/system', matchPrefix: true }, permission: 'core:admin' },
  { item: { text: 'Users', icon: PeopleIcon, path: '/users', matchPrefix: true }, permission: 'core:admin' },
];

type Slot = { id: string; lazy: Lazy; permission?: string; order?: number; moduleId: string };

/**
 * The signed-in frame (07 §7.4.2): MainLayoutFrame over SidebarFrame with the
 * shell's entries then the modules' nav (groups, match rules, badges), the top
 * bar's module items, module banners above the page, and the "g <key>"
 * shortcuts. The one layout ABO's sidebar and top bar are bound to as well.
 */
export function ShellLayout() {
  const { displayName } = useShellContext();
  const { modules } = useMountedModules();
  const navigate = useNavigate();
  const grants = useGrants();
  const items = useMemo(() => buildNav(modules, grants), [modules, grants]);
  const goTable = useMemo(() => buildGoTable(items).map((e) => ({ key: e.key, run: () => navigate(e.href) })), [items, navigate]);
  useKeyboardShortcuts(goTable);
  const navItems = useMemo(
    () => [...SHELL_ENTRIES.filter((e) => grants(e.permission)).map((e) => e.item), ...toSidebarItems(items)],
    [grants, items],
  );

  const collect = (pick: (m: (typeof modules)[number]) => readonly { id: string; lazy: Lazy; permission?: string; order?: number }[] | undefined): Slot[] =>
    modules
      .flatMap((m) => (pick(m) ?? []).map((s) => ({ ...s, moduleId: m.id })))
      .filter((s) => grants(s.permission))
      .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  const banners = collect((m) => m.banners);
  const topBar = collect((m) => m.topBarItems);

  return (
    <MainLayoutFrame
      navItems={navItems}
      topBar={({ onMenuClick, drawerWidth }) => (
        <TopBarFrame
          onMenuClick={onMenuClick}
          drawerWidth={drawerWidth}
          title={displayName}
          onTitleClick={() => navigate('/')}
          actions={topBar.map((t) => (
            <LazySlot key={`${t.moduleId}.${t.id}`} load={t.lazy} label={`${t.moduleId} ${t.id}`} />
          ))}
          account={<UserMenu />}
        />
      )}
      banners={
        <Box data-testid="shell-layout-banners">
          {banners.map((b) => (
            <LazySlot key={`${b.moduleId}.${b.id}`} load={b.lazy} label={`${b.moduleId} banner ${b.id}`} />
          ))}
        </Box>
      }
    >
      <ShellErrorBoundary label="This page">
        <Outlet />
      </ShellErrorBoundary>
    </MainLayoutFrame>
  );
}
