// file: core/web/packages/shell/test/layout/sidebarEquivalence.test.tsx
// version: 0.1.0
// guid: c80e5a37-1b94-4d26-a7f3-9e2d6b0c8154
// last-edited: 2026-11-22

// Pins the selection rules the audiobook app's running sidebar relies on, so a
// later change to SidebarFrame or isSubItemSelected (F9-03 prep adds optional
// match fields) is shown not to move them. Written and passing against the
// frames before that change.

import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import DashboardIcon from '@mui/icons-material/Dashboard';
import { isSubItemSelected } from '../../src/layout/sidebarSelection';
import { SidebarFrame, type SidebarItem } from '../../src/layout/SidebarFrame';
import { renderWithProviders } from './render';

function frame(items: readonly SidebarItem[], entry: string) {
  renderWithProviders(<SidebarFrame items={items} open={false} onClose={() => {}} drawerWidth={240} />, { initialEntries: [entry] });
}

const flat: SidebarItem[] = [
  { text: 'Series', icon: DashboardIcon, path: '/series' },
  { text: 'Library filter', icon: DashboardIcon, path: '/library?reset=1' },
];

describe('SidebarFrame leaf selection is strict path equality', () => {
  it('selects a leaf on exactly its path', () => {
    frame(flat, '/series');
    expect(screen.getByRole('button', { name: 'Series' })).toHaveClass('Mui-selected');
  });

  it('does not select a leaf on a sub-route of its path', () => {
    frame(flat, '/series/9');
    expect(screen.getByRole('button', { name: 'Series' })).not.toHaveClass('Mui-selected');
  });

  it('never selects a leaf whose path carries a query (a pathname has none)', () => {
    frame(flat, '/library');
    expect(screen.getByRole('button', { name: 'Library filter' })).not.toHaveClass('Mui-selected');
  });
});

describe('SidebarFrame group header selection is strict path equality', () => {
  const group: SidebarItem[] = [
    {
      text: 'Library',
      icon: DashboardIcon,
      path: '/library',
      children: [{ text: 'All Books', icon: DashboardIcon, path: '/library?reset=1', matchPath: '/library', matchSearch: '' }],
    },
  ];

  it('selects the header on exactly its path and not on a sub-route', () => {
    frame(group, '/library');
    expect(screen.getByRole('button', { name: 'Library' })).toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'All Books' })).toHaveClass('Mui-selected');
  });

  it('leaves the header unselected on a sub-route', () => {
    frame(group, '/library/9');
    expect(screen.getByRole('button', { name: 'Library' })).not.toHaveClass('Mui-selected');
  });
});

describe('isSubItemSelected without any prefix option', () => {
  it('matches the path only exactly, never a sub-route', () => {
    expect(isSubItemSelected({ path: '/settings' }, '/settings', '')).toBe(true);
    expect(isSubItemSelected({ path: '/settings' }, '/settings/x', '')).toBe(false);
    expect(isSubItemSelected({ path: '/', matchPath: '/' }, '/anything', '')).toBe(false);
  });
});
