// file: web/src/components/layout/Sidebar.tsx
// version: 1.15.1
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-08-07

import { useState } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import {
  Badge,
  Box,
  Collapse,
  Drawer,
  IconButton,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Toolbar,
  Tooltip,
} from '@mui/material';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ChevronLeftIcon from '@mui/icons-material/ChevronLeft';
import ChevronRightIcon from '@mui/icons-material/ChevronRight';
import DashboardIcon from '@mui/icons-material/Dashboard';
import LibraryBooksIcon from '@mui/icons-material/LibraryBooks';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import MonitorIcon from '@mui/icons-material/Monitor';
import SettingsIcon from '@mui/icons-material/Settings';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import MergeTypeIcon from '@mui/icons-material/MergeType';
import BugReportIcon from '@mui/icons-material/BugReport';
import CollectionsBookmarkIcon from '@mui/icons-material/CollectionsBookmark';
import PeopleIcon from '@mui/icons-material/People';
import TimelineIcon from '@mui/icons-material/Timeline';
import WavesIcon from '@mui/icons-material/Waves';
import FactCheckIcon from '@mui/icons-material/FactCheck';
import { useReviewStore } from '../../stores/useReviewStore';

const MOBILE_DRAWER_WIDTH = 240;

interface SidebarProps {
  open: boolean;
  onClose: () => void;
  drawerWidth: number;
  collapsed?: boolean;
  onToggleCollapse?: () => void;
}

const librarySubItems = [
  // path carries ?reset=1 so Library.tsx does a true full reset (page,
  // search, sort, filters, tags) instead of the state-preserving nav most
  // other sidebar links use; matchPath (query-free) drives the selected
  // highlight so it still lights up on a plain /library visit.
  { text: 'All Books', icon: <LibraryBooksIcon />, path: '/library?reset=1', matchPath: '/library' },
  { text: 'In Progress', icon: <MenuBookIcon />, path: '/library?search=read_status:in_progress' },
  { text: 'Finished', icon: <LibraryBooksIcon />, path: '/library?search=read_status:finished' },
  { text: 'Fingerprints', icon: <WavesIcon />, path: '/fingerprints' },
  { text: 'Series', icon: <CollectionsBookmarkIcon />, path: '/series' },
  { text: 'Authors', icon: <PeopleIcon />, path: '/authors' },
];

const menuItems = [
  { text: 'Dashboard', icon: <DashboardIcon />, path: '/dashboard' },
  { text: 'File Browser', icon: <FolderOpenIcon />, path: '/files' },
  { text: 'Works', icon: <MenuBookIcon />, path: '/works' },
  { text: 'Playlists', icon: <MenuBookIcon />, path: '/playlists' },
  { text: 'Activity', icon: <TimelineIcon />, path: '/activity' },
  { text: 'Dedup', icon: <MergeTypeIcon />, path: '/dedup' },
  { text: 'Review', icon: <FactCheckIcon />, path: '/review' },
  { text: 'Gold Labels', icon: <LibraryBooksIcon />, path: '/dedup/labels' },
  { text: 'Diagnostics', icon: <BugReportIcon />, path: '/diagnostics' },
  { text: 'System', icon: <MonitorIcon />, path: '/system' },
  { text: 'Users', icon: <PeopleIcon />, path: '/users' },
  { text: 'Settings', icon: <SettingsIcon />, path: '/settings' },
];

export function Sidebar({ open, onClose, drawerWidth, collapsed = false, onToggleCollapse }: SidebarProps) {
  const navigate = useNavigate();
  const location = useLocation();
  // Live pending-review count → a Badge on the Review nav icon. Read from the
  // store in render (NOT baked into the static menuItems array, whose icons are
  // built once at module load and would freeze the count at 0).
  const reviewCount = useReviewStore((s) => s.count);

  const [libraryOpen, setLibraryOpen] = useState(true);

  // Wrap a menu item's icon in a count Badge for the Review entry.
  const renderMenuIcon = (item: (typeof menuItems)[number]) => {
    if (item.path === '/review') {
      return (
        <Badge badgeContent={reviewCount || undefined} color="warning">
          {item.icon}
        </Badge>
      );
    }
    return item.icon;
  };

  const handleNavigation = (path: string) => {
    navigate(path);
    onClose();
  };

  const buildContent = (isCollapsed: boolean, showToggle: boolean) => (
    <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <Toolbar />
      <List sx={{ flexGrow: 1, overflow: 'hidden' }}>
        {/* Dashboard */}
        <ListItem disablePadding>
          <Tooltip title={isCollapsed ? 'Dashboard' : ''} placement="right">
            <ListItemButton
              selected={location.pathname === '/dashboard'}
              onClick={() => handleNavigation('/dashboard')}
              sx={{ justifyContent: isCollapsed ? 'center' : 'flex-start' }}
            >
              <ListItemIcon sx={{ minWidth: isCollapsed ? 0 : undefined }}>
                <DashboardIcon />
              </ListItemIcon>
              {!isCollapsed && <ListItemText primary="Dashboard" />}
            </ListItemButton>
          </Tooltip>
        </ListItem>

        {/* Library group */}
        {isCollapsed ? (
          <ListItem disablePadding>
            <Tooltip title="Library" placement="right">
              <ListItemButton
                selected={location.pathname.startsWith('/library') || location.pathname === '/fingerprints' || location.pathname === '/series' || location.pathname === '/authors'}
                onClick={() => handleNavigation('/library')}
                sx={{ justifyContent: 'center' }}
              >
                <ListItemIcon sx={{ minWidth: 0 }}>
                  <LibraryBooksIcon />
                </ListItemIcon>
              </ListItemButton>
            </Tooltip>
          </ListItem>
        ) : (
          <>
            <ListItem disablePadding secondaryAction={
              <IconButton edge="end" size="small" onClick={() => setLibraryOpen(!libraryOpen)}>
                {libraryOpen ? <ExpandLessIcon /> : <ExpandMoreIcon />}
              </IconButton>
            }>
              <ListItemButton
                selected={location.pathname === '/library'}
                onClick={() => handleNavigation('/library')}
                sx={{ pr: 5 }}
              >
                <ListItemIcon><LibraryBooksIcon /></ListItemIcon>
                <ListItemText primary="Library" />
              </ListItemButton>
            </ListItem>
            <Collapse in={libraryOpen} timeout="auto" unmountOnExit>
              <List component="div" disablePadding>
                {librarySubItems.map((item) => (
                  <ListItem key={item.text} disablePadding>
                    <ListItemButton
                      sx={{ pl: 4 }}
                      selected={location.pathname === (item.matchPath ?? item.path)}
                      onClick={() => handleNavigation(item.path)}
                    >
                      <ListItemIcon>{item.icon}</ListItemIcon>
                      <ListItemText primary={item.text} />
                    </ListItemButton>
                  </ListItem>
                ))}
              </List>
            </Collapse>
          </>
        )}

        {/* Other menu items */}
        {menuItems.slice(1).map((item) => (
          <ListItem key={item.text} disablePadding>
            <Tooltip title={isCollapsed ? item.text : ''} placement="right">
              <ListItemButton
                selected={location.pathname === item.path}
                onClick={() => handleNavigation(item.path)}
                sx={{ justifyContent: isCollapsed ? 'center' : 'flex-start' }}
              >
                <ListItemIcon sx={{ minWidth: isCollapsed ? 0 : undefined }}>
                  {renderMenuIcon(item)}
                </ListItemIcon>
                {!isCollapsed && <ListItemText primary={item.text} />}
              </ListItemButton>
            </Tooltip>
          </ListItem>
        ))}
      </List>

      {showToggle && onToggleCollapse && (
        <Box sx={{ p: 0.5, borderTop: 1, borderColor: 'divider' }}>
          <Tooltip title={isCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} placement="right">
            <IconButton
              onClick={onToggleCollapse}
              size="small"
              sx={{ width: '100%', borderRadius: 1 }}
            >
              {isCollapsed ? <ChevronRightIcon /> : <ChevronLeftIcon />}
            </IconButton>
          </Tooltip>
        </Box>
      )}
    </Box>
  );

  return (
    <Box
      component="nav"
      sx={{ width: { sm: drawerWidth }, flexShrink: { sm: 0 } }}
    >
      {/* Mobile drawer — always full width, no collapse */}
      <Drawer
        variant="temporary"
        open={open}
        onClose={onClose}
        ModalProps={{ keepMounted: true }}
        sx={{
          display: { xs: 'block', sm: 'none' },
          '& .MuiDrawer-paper': {
            boxSizing: 'border-box',
            width: MOBILE_DRAWER_WIDTH,
          },
        }}
      >
        {buildContent(false, false)}
      </Drawer>
      {/* Permanent drawer — supports collapse to icon-only */}
      <Drawer
        variant="permanent"
        sx={{
          display: { xs: 'none', sm: 'block' },
          '& .MuiDrawer-paper': {
            boxSizing: 'border-box',
            width: drawerWidth,
            overflowX: 'hidden',
            transition: (theme) =>
              theme.transitions.create('width', {
                easing: theme.transitions.easing.sharp,
                duration: collapsed
                  ? theme.transitions.duration.leavingScreen
                  : theme.transitions.duration.enteringScreen,
              }),
          },
        }}
        open
      >
        {buildContent(collapsed, true)}
      </Drawer>
    </Box>
  );
}
