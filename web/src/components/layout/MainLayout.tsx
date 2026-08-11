// file: web/src/components/layout/MainLayout.tsx
// version: 1.5.0
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-08-10
import { useState, ReactNode } from 'react';
import { Box } from '@mui/material';
import { Sidebar } from './Sidebar';
import { TopBar } from './TopBar';
import { ReviewBanner } from '../ReviewBanner';
import { usePendingFileOps } from '../../hooks/usePendingFileOps';

interface MainLayoutProps {
  children: ReactNode;
}

const DRAWER_WIDTH = 240;
const COLLAPSED_WIDTH = 56;

export function MainLayout({ children }: MainLayoutProps) {
  const [mobileOpen, setMobileOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);

  // Global poller — fires the "All tag writes complete" toast on every page
  // even if the user is not on Activity Log.
  usePendingFileOps({ fireToasts: true });

  const handleDrawerToggle = () => {
    setMobileOpen(!mobileOpen);
  };

  const effectiveWidth = sidebarCollapsed ? COLLAPSED_WIDTH : DRAWER_WIDTH;

  return (
    <Box sx={{ display: 'flex', width: '100%' }}>
      <TopBar onMenuClick={handleDrawerToggle} drawerWidth={effectiveWidth} />
      <Sidebar
        open={mobileOpen}
        onClose={handleDrawerToggle}
        drawerWidth={effectiveWidth}
        collapsed={sidebarCollapsed}
        onToggleCollapse={() => setSidebarCollapsed((c) => !c)}
      />
      <Box
        component="main"
        sx={theme => ({
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
          })
        })}
      >
        <ReviewBanner />
        {children}
        <Box aria-hidden="true" sx={{ height: 56 }} />
      </Box>
    </Box>
  );
}
