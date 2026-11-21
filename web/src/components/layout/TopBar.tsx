// file: web/src/components/layout/TopBar.tsx
// version: 1.8.2
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-11-07

import { useNavigate } from 'react-router';
import { IconButton, Tooltip } from '@mui/material';
import SettingsIcon from '@mui/icons-material/Settings';
import { eventSourceConnection } from '../../modules/books/layout/connection';
import { LibrarySearch } from '../../modules/books/layout/LibrarySearch';
import { OperationsIndicator } from './OperationsIndicator';
import { TopBarFrame } from './TopBarFrame';
import { UserMenu } from './UserMenu';

interface TopBarProps {
  onMenuClick: () => void;
  drawerWidth: number;
}

/** The top bar bound to the audiobook title, library search, operations indicator, settings and user menu; F9-03 re-points importers to the shell and deletes this file. */
export function TopBar({ onMenuClick, drawerWidth }: TopBarProps) {
  const navigate = useNavigate();
  return (
    <TopBarFrame
      onMenuClick={onMenuClick}
      drawerWidth={drawerWidth}
      title="Audiobook Organizer"
      onTitleClick={() => navigate('/dashboard')}
      connection={eventSourceConnection}
      center={<LibrarySearch />}
      actions={
        <>
          <OperationsIndicator />
          <Tooltip title="Settings">
            <IconButton color="inherit" aria-label="settings" onClick={() => navigate('/settings')}>
              <SettingsIcon />
            </IconButton>
          </Tooltip>
        </>
      }
      account={<UserMenu />}
    />
  );
}
