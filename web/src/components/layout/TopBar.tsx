// file: web/src/components/layout/TopBar.tsx
// version: 1.7.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b

import { useEffect, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import {
  AppBar,
  Box,
  Chip,
  InputBase,
  IconButton,
  Toolbar,
  Tooltip,
  Typography,
} from '@mui/material';
import MenuIcon from '@mui/icons-material/Menu';
import Brightness4Icon from '@mui/icons-material/Brightness4';
import Brightness7Icon from '@mui/icons-material/Brightness7';
import SearchIcon from '@mui/icons-material/Search';
import SettingsIcon from '@mui/icons-material/Settings';
import { eventSourceManager } from '../../services/eventSourceManager';
import { useAppStore } from '../../stores/useAppStore';
import { OperationsIndicator } from './OperationsIndicator';
import { UserMenu } from './UserMenu';

interface TopBarProps {
  onMenuClick: () => void;
  drawerWidth: number;
}

export function TopBar({ onMenuClick, drawerWidth }: TopBarProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const [connectionState, setConnectionState] = useState<
    'open' | 'reconnecting' | 'closed' | 'error'
  >('open');
  const [connectionMessage, setConnectionMessage] = useState<string | null>(
    null
  );
  const [searchQuery, setSearchQuery] = useState('');
  const lastStateRef = useRef(connectionState);
  const timeoutRef = useRef<number | null>(null);
  const isUnmountedRef = useRef(false);
  const themeMode = useAppStore((state) => state.themeMode);
  const toggleThemeMode = useAppStore((state) => state.toggleThemeMode);

  useEffect(() => {
    const unsubscribe = eventSourceManager.subscribe(
      () => undefined,
      (status) => {
        const previous = lastStateRef.current;
        lastStateRef.current = status.state;
        setConnectionState(status.state);

        if (status.state === 'open') {
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
        } else if (
          status.state === 'reconnecting' ||
          status.state === 'error'
        ) {
          setConnectionMessage('Connection lost');
        }
      }
    );

    return () => {
      unsubscribe();
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  useEffect(() => {
    if (!location.pathname.startsWith('/library')) {
      return;
    }
    const params = new URLSearchParams(location.search);
    setSearchQuery(params.get('search') || '');
  }, [location.pathname, location.search]);

  const submitSearch = () => {
    const params = new URLSearchParams();
    if (searchQuery.trim()) {
      params.set('search', searchQuery.trim());
    }
    navigate({
      pathname: '/library',
      search: params.toString() ? `?${params.toString()}` : '',
    });
  };

  return (
    <AppBar
      position="fixed"
      sx={theme => ({
        width: { sm: `calc(100% - ${drawerWidth}px)` },
        ml: { sm: `${drawerWidth}px` },
        transition: theme.transitions.create(['width', 'margin'], {
          easing: theme.transitions.easing.sharp,
          duration: theme.transitions.duration.leavingScreen,
        })
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
          onClick={() => navigate('/dashboard')}
          sx={{ mr: 2, cursor: 'pointer', userSelect: 'none' }}
        >
          Audiobook Organizer
        </Typography>
        <Box
          sx={{
            display: { xs: 'none', md: 'flex' },
            alignItems: 'center',
            px: 1.5,
            py: 0.5,
            borderRadius: 2,
            bgcolor: 'rgba(255,255,255,0.15)',
            flexGrow: 1,
            maxWidth: 520,
            mr: 2,
          }}
        >
          <SearchIcon fontSize="small" sx={{ mr: 1, opacity: 0.8 }} />
          <InputBase
            value={searchQuery}
            onChange={(event) => setSearchQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault();
                submitSearch();
              }
            }}
            placeholder="Search library..."
            inputProps={{ 'aria-label': 'Search library' }}
            sx={{ color: 'inherit', width: '100%' }}
          />
        </Box>
        <Tooltip title="Search library">
          <IconButton
            color="inherit"
            aria-label="search library"
            onClick={submitSearch}
            sx={{ mr: 1, display: { xs: 'inline-flex', md: 'none' } }}
          >
            <SearchIcon />
          </IconButton>
        </Tooltip>
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
        <OperationsIndicator />
        <Tooltip title="Settings">
          <IconButton
            color="inherit"
            aria-label="settings"
            onClick={() => navigate('/settings')}
          >
            <SettingsIcon />
          </IconButton>
        </Tooltip>
        <Tooltip title="Toggle color mode">
          <IconButton
            color="inherit"
            aria-label="toggle color mode"
            onClick={toggleThemeMode}
          >
            {themeMode === 'dark' ? (
              <Brightness7Icon />
            ) : (
              <Brightness4Icon />
            )}
          </IconButton>
        </Tooltip>
        <UserMenu />
      </Toolbar>
    </AppBar>
  );
}
