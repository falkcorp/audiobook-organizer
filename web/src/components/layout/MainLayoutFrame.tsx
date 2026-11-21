// file: web/src/components/layout/MainLayoutFrame.tsx
// version: 0.1.0
// guid: 7b4e1d93-2a58-4f06-8c1b-a0d9e3f5c672
// last-edited: 2026-11-21

// The audiobook-free page layout (F9-02): a top bar, the sidebar, and the
// scrolling main column, with the collapse and mobile-drawer state. MainLayout.tsx
// binds the books nav entries, top bar and banners; this file moves into
// core/web/packages/shell/src/layout/ and names no audiobook import. (The
// "global poller" its MainLayout called here is now a banner-slot entry.)

import { useState, type ReactNode } from 'react';
import { Box } from '@mui/material';
import { SidebarFrame, type SidebarItem } from './SidebarFrame';

const DRAWER_WIDTH = 240;
const COLLAPSED_WIDTH = 56;

export interface MainLayoutFrameProps {
  children: ReactNode;
  navItems: readonly SidebarItem[];
  /** Renders the top bar; handed the drawer toggle and the sidebar's current width. */
  topBar: (props: { onMenuClick: () => void; drawerWidth: number }) => ReactNode;
  /** Above the page, inside the scrolling column. Entries render null when idle. */
  banners?: ReactNode;
}

export function MainLayoutFrame({ children, navItems, topBar, banners }: MainLayoutFrameProps) {
  const [mobileOpen, setMobileOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);

  const handleDrawerToggle = () => {
    setMobileOpen(!mobileOpen);
  };

  const effectiveWidth = sidebarCollapsed ? COLLAPSED_WIDTH : DRAWER_WIDTH;

  return (
    <Box sx={{ display: 'flex', width: '100%' }}>
      {topBar({ onMenuClick: handleDrawerToggle, drawerWidth: effectiveWidth })}
      <SidebarFrame
        items={navItems}
        open={mobileOpen}
        onClose={handleDrawerToggle}
        drawerWidth={effectiveWidth}
        collapsed={sidebarCollapsed}
        onToggleCollapse={() => setSidebarCollapsed((c) => !c)}
      />
      <Box
        component="main"
        sx={(theme) => ({
          flexGrow: 1,
          p: 3,
          width: { sm: `calc(100% - ${effectiveWidth}px)` },
          mt: 8,
          height: '100vh',
          overflow: 'auto',
          transition: theme.transitions.create(['width', 'margin'], {
            easing: theme.transitions.easing.sharp,
            duration: sidebarCollapsed
              ? theme.transitions.duration.leavingScreen
              : theme.transitions.duration.enteringScreen,
          }),
        })}
      >
        {banners}
        {children}
        <Box aria-hidden="true" sx={{ height: 56 }} />
      </Box>
    </Box>
  );
}
