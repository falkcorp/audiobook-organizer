// file: core/web/packages/shell/test/layout/sidebarMatch.test.tsx
// version: 0.1.0
// guid: 91c3e7a5-2d48-4b06-8f1e-5a0b7d9c3e64
// last-edited: 2026-11-22

// F9-03 prep: the optional matchPrefix, and match fields honoured on leaf and
// group-header entries. Entries without them select exactly as before.

import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import DashboardIcon from '@mui/icons-material/Dashboard';
import { isSubItemSelected } from '../../src/layout/sidebarSelection';
import { SidebarFrame } from '../../src/layout/SidebarFrame';
import { renderWithProviders } from './render';

describe('isSubItemSelected matchPrefix', () => {
  it('matches the path and its sub-routes only when asked', () => {
    expect(isSubItemSelected({ path: '/settings', matchPrefix: true }, '/settings/a.b', '')).toBe(true);
    expect(isSubItemSelected({ path: '/settings', matchPrefix: true }, '/settingsx', '')).toBe(false);
    expect(isSubItemSelected({ path: '/settings' }, '/settings/a.b', '')).toBe(false);
    expect(isSubItemSelected({ path: '/', matchPrefix: true }, '/anything', '')).toBe(false);
    expect(isSubItemSelected({ path: '/', matchPrefix: true }, '/', '')).toBe(true);
  });
});

describe('SidebarFrame leaf and group selection', () => {
  const frame = (items: Parameters<typeof SidebarFrame>[0]['items'], entry: string) =>
    renderWithProviders(<SidebarFrame items={items} open={false} onClose={() => {}} drawerWidth={240} />, { initialEntries: [entry] });

  it('honours matchPath on a leaf whose path carries a query', () => {
    frame([{ text: 'Vehicles', icon: DashboardIcon, path: '/b/vehicles?sort=speed', matchPath: '/b/vehicles' }], '/b/vehicles');
    expect(screen.getByRole('button', { name: 'Vehicles' })).toHaveClass('Mui-selected');
  });

  it('honours matchPrefix on a leaf', () => {
    frame([{ text: 'Settings', icon: DashboardIcon, path: '/settings', matchPrefix: true }], '/settings/x');
    expect(screen.getByRole('button', { name: 'Settings' })).toHaveClass('Mui-selected');
  });

  it('leaves a leaf with no match fields on strict path equality', () => {
    frame([{ text: 'Settings', icon: DashboardIcon, path: '/settings' }], '/settings/x');
    expect(screen.getByRole('button', { name: 'Settings' })).not.toHaveClass('Mui-selected');
  });
});
