// file: core/web/packages/shell/src/layout/TopBarFrame.tsx
// version: 0.1.0
// guid: 9d3f5a71-4e28-4c06-b1a9-7c0e2d8f6b35
// last-edited: 2026-11-21

// The audiobook-free top bar (F9-02): the app bar, the drawer button, a title,
// the connection chip, the colour-mode toggle, and three slots. TopBar.tsx
// binds the books search, operations indicator, settings button and user menu
// into them; this file names no audiobook route, service or component. The connection feed is a prop
// (`connection`) instead of an import of the SSE manager.

import { useEffect, useRef, useState, type ReactNode } from 'react';
import {
  AppBar,
  Box,
  Chip,
  IconButton,
  Toolbar,
  Tooltip,
  Typography,
} from '@mui/material';
import MenuIcon from '@mui/icons-material/Menu';
import Brightness4Icon from '@mui/icons-material/Brightness4';
import Brightness7Icon from '@mui/icons-material/Brightness7';
import { useColorScheme } from '@mui/material/styles';

export type ConnectionState = 'open' | 'reconnecting' | 'closed' | 'error';

/**
 * A feed of the live connection's state. `subscribe` must have a stable
 * identity (module-level), because the frame subscribes in an effect keyed on
 * it, and returns the unsubscribe.
 */
export interface ConnectionFeed {
  subscribe: (onState: (state: ConnectionState) => void) => () => void;
}

export interface TopBarFrameProps {
  onMenuClick: () => void;
  drawerWidth: number;
  title: string;
  onTitleClick?: () => void;
  connection?: ConnectionFeed;
  /** Between the title and the right-aligned controls: search, breadcrumbs. */
  center?: ReactNode;
  /** Right-aligned, before the colour-mode toggle. */
  actions?: ReactNode;
  /** Right-aligned, after the colour-mode toggle: the account menu. */
  account?: ReactNode;
}

export function TopBarFrame({
  onMenuClick,
  drawerWidth,
  title,
  onTitleClick,
  connection,
  center,
  actions,
  account,
}: TopBarFrameProps) {
  const [connectionState, setConnectionState] = useState<ConnectionState>('open');
  const [connectionMessage, setConnectionMessage] = useState<string | null>(null);
  const lastStateRef = useRef(connectionState);
  const timeoutRef = useRef<number | null>(null);
  const isUnmountedRef = useRef(false);
  // Mode is owned by MUI now rather than the app store, so the toggle and the
  // CSS variables can never disagree. `mode` is undefined on the very first
  // render before MUI reads storage; treat that as dark, matching
  // defaultColorScheme.
  const { mode, setMode } = useColorScheme();
  const isDark = mode !== 'light';
  const toggleThemeMode = () => setMode(isDark ? 'light' : 'dark');

  useEffect(() => {
    if (!connection) return undefined;
    const unsubscribe = connection.subscribe((state) => {
      const previous = lastStateRef.current;
      lastStateRef.current = state;
      setConnectionState(state);

      if (state === 'open') {
        if (previous !== 'open') {
          setConnectionMessage('Connection restored');
          // Clear existing timeout and schedule new one
          if (timeoutRef.current) {
            clearTimeout(timeoutRef.current);
          }
          timeoutRef.current = window.setTimeout(() => {
            if (!isUnmountedRef.current) {
              setConnectionMessage(null);
            }
            timeoutRef.current = null;
          }, 3000);
        }
      } else if (state === 'reconnecting' || state === 'error') {
        setConnectionMessage('Connection lost');
      }
    });

    return () => {
      unsubscribe();
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, [connection]);

  return (
    <AppBar
      position="fixed"
      sx={(theme) => ({
        width: { sm: `calc(100% - ${drawerWidth}px)` },
        ml: { sm: `${drawerWidth}px` },
        transition: theme.transitions.create(['width', 'margin'], {
          easing: theme.transitions.easing.sharp,
          duration: theme.transitions.duration.leavingScreen,
        }),
      })}
    >
      <Toolbar>
        <IconButton
          color="inherit"
          aria-label="open drawer"
          edge="start"
          onClick={onMenuClick}
          sx={{ mr: 2, display: { sm: 'none' } }}
        >
          <MenuIcon />
        </IconButton>
        <Typography
          variant="h6"
          noWrap
          component="div"
          onClick={onTitleClick}
          sx={{ mr: 2, cursor: onTitleClick ? 'pointer' : undefined, userSelect: 'none' }}
        >
          {title}
        </Typography>
        {center}
        {/* Spacer pushes everything after this to the right */}
        <Box sx={{ flexGrow: 1 }} />
        {connectionMessage && (
          <Chip
            label={connectionMessage}
            color={connectionState === 'open' ? 'success' : 'warning'}
            size="small"
            variant="outlined"
            sx={{ mr: 1 }}
          />
        )}
        {actions}
        <Tooltip title="Toggle color mode">
          <IconButton color="inherit" aria-label="toggle color mode" onClick={toggleThemeMode}>
            {isDark ? <Brightness7Icon /> : <Brightness4Icon />}
          </IconButton>
        </Tooltip>
        {account}
      </Toolbar>
    </AppBar>
  );
}
