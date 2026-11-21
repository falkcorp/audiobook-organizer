// file: core/web/packages/shell/test/layout/SidebarFrame.test.tsx
// version: 0.1.0
// guid: 0c9e4b72-6a15-4d38-8f21-b7d3a5e1c964
// last-edited: 2026-11-21

// SidebarFrame renders whatever items it is handed and names no audiobook
// route, store or icon, so this test feeds it Gotham entries of its own.

import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import DashboardIcon from '@mui/icons-material/Dashboard';
import MapIcon from '@mui/icons-material/Map';
import { renderWithProviders } from './render';
import { SidebarFrame, type SidebarItem } from '../../src/layout/SidebarFrame';

const navigateSpy = vi.fn();
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigateSpy };
});

let alerts = 4;
const useAlerts = () => alerts;

const items: readonly SidebarItem[] = [
  { text: 'Batcave', icon: DashboardIcon, path: '/batcave' },
  {
    text: 'Gotham',
    icon: MapIcon,
    path: '/gotham',
    children: [
      { text: 'Arkham', icon: MapIcon, path: '/gotham?ward=arkham', matchPath: '/gotham', matchSearch: 'arkham' },
      { text: 'Narrows', icon: MapIcon, path: '/narrows' },
    ],
  },
  { text: 'Signal', icon: DashboardIcon, path: '/signal', useBadge: useAlerts },
];

function render(opts: { collapsed?: boolean; at?: string; onClose?: () => void } = {}) {
  return renderWithProviders(
    <SidebarFrame items={items} open={false} onClose={opts.onClose ?? vi.fn()} drawerWidth={240} collapsed={opts.collapsed} />,
    { initialEntries: [opts.at ?? '/batcave'] },
  );
}

describe('SidebarFrame', () => {
  it('renders the handed items in order, with a group\'s children after its header', () => {
    render();
    const names = screen
      .getAllByRole('button')
      .map((b) => b.getAttribute('aria-label') || b.textContent || '')
      .filter((n) => n !== '');
    expect(names).toEqual(['Batcave', 'Gotham', 'Arkham', 'Narrows', '4Signal']);
  });

  it('navigates to an item\'s path and closes the mobile drawer', () => {
    const onClose = vi.fn();
    navigateSpy.mockReset();
    render({ onClose });
    fireEvent.click(screen.getByRole('button', { name: 'Narrows' }));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/narrows');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('calls an item\'s badge hook in render and shows its value, hiding a zero', () => {
    alerts = 4;
    const { unmount } = render();
    expect(within(screen.getByRole('button', { name: /^Signal/ })).getByText('4')).toBeInTheDocument();
    unmount();
    alerts = 0;
    render();
    expect(within(screen.getByRole('button', { name: /^Signal/ })).queryByText('0')).not.toBeInTheDocument();
  });

  it('selects a child by its decoded search param, not its raw path', () => {
    render({ at: '/gotham?ward=arkham&search=arkham&page=1' });
    expect(screen.getByRole('button', { name: 'Arkham' })).toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'Narrows' })).not.toHaveClass('Mui-selected');
  });

  it('collapsed: a group is one icon button whose menu lists the children', () => {
    navigateSpy.mockReset();
    render({ collapsed: true });
    fireEvent.click(screen.getByRole('button', { name: 'Gotham' }));
    const menu = screen.getByRole('menu');
    expect(within(menu).getAllByRole('menuitem').map((m) => m.textContent)).toEqual(['Arkham', 'Narrows']);
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Narrows' }));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/narrows');
  });

  it('collapsed: the group icon stays selected on a child\'s page', () => {
    render({ collapsed: true, at: '/narrows' });
    expect(screen.getByRole('button', { name: 'Gotham' })).toHaveClass('Mui-selected');
  });
});
