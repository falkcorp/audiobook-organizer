// file: core/web/packages/shell/test/layout/MainLayoutFrame.test.tsx
// version: 0.1.0
// guid: 8e1c5a27-9d43-4b60-a2f7-c3b8d0e4f159
// last-edited: 2026-11-21

// MainLayoutFrame renders the nav entries, top bar and banners it is handed and
// names no audiobook import, so this test feeds it Gotham ones of its own.

import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import DashboardIcon from '@mui/icons-material/Dashboard';
import { renderWithProviders } from './render';
import { MainLayoutFrame, type MainLayoutFrameProps } from '../../src/layout/MainLayoutFrame';

const items = [{ text: 'Batcave', icon: DashboardIcon, path: '/batcave' }];

function render(topBar: MainLayoutFrameProps['topBar'] = vi.fn(() => <div data-testid="bar" />)) {
  renderWithProviders(
    <MainLayoutFrame navItems={items} topBar={topBar} banners={<div data-testid="banner">Bat-Signal</div>}>
      <div data-testid="page">Gotham</div>
    </MainLayoutFrame>,
  );
  return topBar;
}

describe('MainLayoutFrame', () => {
  it('renders the banners above the page inside <main>, and the nav entries', () => {
    render();
    const main = screen.getByRole('main');
    const banner = within(main).getByTestId('banner');
    const page = within(main).getByTestId('page');
    expect(banner.compareDocumentPosition(page) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Batcave' })).toBeInTheDocument();
  });

  it('hands the top bar the full drawer width, then the collapsed width after a collapse', () => {
    const topBar = vi.fn(() => <div data-testid="bar" />);
    render(topBar);
    expect(topBar).toHaveBeenLastCalledWith(expect.objectContaining({ drawerWidth: 240 }));
    fireEvent.click(screen.getByRole('button', { name: 'Collapse sidebar' }));
    expect(topBar).toHaveBeenLastCalledWith(expect.objectContaining({ drawerWidth: 56 }));
    fireEvent.click(screen.getByRole('button', { name: 'Expand sidebar' }));
    expect(topBar).toHaveBeenLastCalledWith(expect.objectContaining({ drawerWidth: 240 }));
  });

  it('opens and closes the mobile drawer through the top bar callback', () => {
    render(
      vi.fn(({ onMenuClick }: { onMenuClick: () => void }) => (
        <button type="button" onClick={onMenuClick}>
          menu
        </button>
      )),
    );
    // Closed, the permanent drawer (with its collapse toggle) is the visible one.
    expect(screen.getByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'menu' }));
    // Open, the modal drawer hides the rest of the page and has no toggle.
    expect(screen.queryByRole('button', { name: 'Collapse sidebar' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Batcave' })).toBeInTheDocument();
  });

  it('renders with no banners', () => {
    renderWithProviders(
      <MainLayoutFrame navItems={items} topBar={() => null}>
        <div data-testid="page" />
      </MainLayoutFrame>,
    );
    expect(screen.getByTestId('page')).toBeInTheDocument();
  });
});
