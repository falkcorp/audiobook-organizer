// file: web/src/components/layout/SidebarFrame.tsx
// version: 0.1.0
// guid: 2f6a9d83-1c47-4b05-8e72-d5a0b3c9f164
// last-edited: 2026-11-21

// The audiobook-free sidebar (F9-02): the drawer, the collapse toggle, the
// selection rule and the Library-style groups, over a list of items it is
// handed. Sidebar.tsx binds the books items; this file moves into
// core/web/packages/shell/src/layout/ and names no audiobook route, store or
// icon. Moved here from Sidebar.tsx's arrays and its useReviewStore read.
import { useState, type ComponentType, type ReactNode } from 'react';
import { useNavigate, useLocation } from 'react-router';
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
  Menu,
  MenuItem,
  Toolbar,
  Tooltip,
  type SvgIconProps,
} from '@mui/material';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ChevronLeftIcon from '@mui/icons-material/ChevronLeft';
import ChevronRightIcon from '@mui/icons-material/ChevronRight';
import { isSubItemSelected } from './sidebarSelection';

const MOBILE_DRAWER_WIDTH = 240;

/**
 * One sidebar entry. `children` makes it a group: a header row that navigates
 * to `path` and expands in place, or, collapsed, a single icon whose menu lists
 * the children. `matchPath` and `matchSearch` decide a child's selected state
 * (sidebarSelection.ts); `useBadge` is a hook (named use*, stable identity) the
 * frame calls inside a component of its own per entry, so rules-of-hooks hold.
 */
export interface SidebarItem {
  text: string;
  icon: ComponentType<SvgIconProps>;
  path: string;
  matchPath?: string;
  matchSearch?: string;
  useBadge?: () => number | undefined;
  children?: readonly SidebarItem[];
}

export interface SidebarFrameProps {
  items: readonly SidebarItem[];
  open: boolean;
  onClose: () => void;
  drawerWidth: number;
  collapsed?: boolean;
  onToggleCollapse?: () => void;
}

/** The entry's icon, wrapped in a count badge when the entry has a badge hook. */
function ItemIcon({ item }: { item: SidebarItem }) {
  const Icon = item.icon;
  if (!item.useBadge) return <Icon />;
  return <BadgedIcon useBadge={item.useBadge} icon={<Icon />} />;
}

// A component per badge: the hook is called unconditionally at its top, and
// the count is read in render, not baked into an icon built once at module
// load (which would freeze it at its first value).
function BadgedIcon({ useBadge, icon }: { useBadge: () => number | undefined; icon: ReactNode }) {
  const count = useBadge();
  return (
    <Badge badgeContent={count || undefined} color="warning">
      {icon}
    </Badge>
  );
}

function pathOf(item: { path: string; matchPath?: string }): string {
  return (item.matchPath ?? item.path).split('?')[0];
}

interface EntryProps {
  item: SidebarItem;
  isCollapsed: boolean;
  go: (path: string) => void;
}

function LeafEntry({ item, isCollapsed, go }: EntryProps) {
  const location = useLocation();
  return (
    <ListItem disablePadding>
      <Tooltip title={isCollapsed ? item.text : ''} placement="right">
        <ListItemButton
          selected={location.pathname === item.path}
          onClick={() => go(item.path)}
          sx={[
            isCollapsed
              ? {
                  justifyContent: 'center',
                }
              : {
                  justifyContent: 'flex-start',
                },
          ]}
        >
          <ListItemIcon
            sx={[
              isCollapsed
                ? {
                    minWidth: 0,
                  }
                : {
                    minWidth: null,
                  },
            ]}
          >
            <ItemIcon item={item} />
          </ListItemIcon>
          {!isCollapsed && <ListItemText primary={item.text} />}
        </ListItemButton>
      </Tooltip>
    </ListItem>
  );
}

function GroupEntry({ item, isCollapsed, go }: EntryProps) {
  const location = useLocation();
  const children = item.children ?? [];
  const [groupOpen, setGroupOpen] = useState(true);
  const [menuAnchor, setMenuAnchor] = useState<null | HTMLElement>(null);
  const Icon = item.icon;

  if (isCollapsed) {
    return (
      <ListItem disablePadding>
        <Tooltip title={item.text} placement="right">
          <ListItemButton
            selected={
              location.pathname.startsWith(item.path) ||
              children.some((c) => location.pathname === pathOf(c))
            }
            onClick={(e) => setMenuAnchor(e.currentTarget)}
            sx={{ justifyContent: 'center' }}
          >
            <ListItemIcon sx={{ minWidth: 0 }}>
              <Icon />
            </ListItemIcon>
          </ListItemButton>
        </Tooltip>
        <Menu
          anchorEl={menuAnchor}
          open={Boolean(menuAnchor)}
          onClose={() => setMenuAnchor(null)}
          // The stuck-modal mitigation this Menu used to set inline
          // (`slotProps={{ transition: { exit: false } }}`) now lives in
          // theme.ts's MuiMenu defaultProps and reaches every Menu,
          // including this one -- pinned by theme.slot-merge.test.ts,
          // which feeds the real defaults through MUI's real resolveProps.
          // Removed here rather than kept as belt-and-braces: two copies of
          // the same mitigation invite the two to drift, and a local copy
          // implies this Menu is special when it is not. The reasoning and
          // the measurements live in theme.ts.
          transformOrigin={{ horizontal: 'left', vertical: 'top' }}
          anchorOrigin={{ horizontal: 'right', vertical: 'top' }}
        >
          {children.map((child) => {
            const ChildIcon = child.icon;
            return (
              <MenuItem
                key={child.text}
                selected={isSubItemSelected(child, location.pathname, location.search)}
                onClick={() => {
                  setMenuAnchor(null);
                  go(child.path);
                }}
              >
                <ListItemIcon>
                  <ChildIcon />
                </ListItemIcon>
                <ListItemText primary={child.text} />
              </MenuItem>
            );
          })}
        </Menu>
      </ListItem>
    );
  }

  return (
    <>
      <ListItem
        disablePadding
        secondaryAction={
          <IconButton edge="end" size="small" onClick={() => setGroupOpen(!groupOpen)}>
            {groupOpen ? <ExpandLessIcon /> : <ExpandMoreIcon />}
          </IconButton>
        }
      >
        <ListItemButton
          selected={location.pathname === item.path}
          onClick={() => go(item.path)}
          sx={{ pr: 5 }}
        >
          <ListItemIcon>
            <Icon />
          </ListItemIcon>
          <ListItemText primary={item.text} />
        </ListItemButton>
      </ListItem>
      <Collapse in={groupOpen} timeout="auto" unmountOnExit>
        <List component="div" disablePadding>
          {children.map((child) => {
            const ChildIcon = child.icon;
            return (
              <ListItem key={child.text} disablePadding>
                <ListItemButton
                  sx={{ pl: 4 }}
                  selected={isSubItemSelected(child, location.pathname, location.search)}
                  onClick={() => go(child.path)}
                >
                  <ListItemIcon>
                    <ChildIcon />
                  </ListItemIcon>
                  <ListItemText primary={child.text} />
                </ListItemButton>
              </ListItem>
            );
          })}
        </List>
      </Collapse>
    </>
  );
}

export function SidebarFrame({
  items,
  open,
  onClose,
  drawerWidth,
  collapsed = false,
  onToggleCollapse,
}: SidebarFrameProps) {
  const navigate = useNavigate();

  const handleNavigation = (path: string) => {
    navigate(path);
    onClose();
  };

  const buildContent = (isCollapsed: boolean, showToggle: boolean) => (
    <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <Toolbar />
      {/*
        This List is the drawer's scroll container, and it has to be: the parent
        Box is height:100% so the Drawer paper itself never overflows, which
        means the paper's own scrollbar can never appear. With `overflow:
        'hidden'` here (the value used until 2026-09-08) any nav item past the
        fold was clipped and simply unreachable on a short viewport — no
        scrollbar anywhere, no way to get to it.

        overflowX stays hidden so the width transition on the collapsed drawer
        can't produce a horizontal bar, and scrollbarGutter keeps the icon-only
        (collapsed) layout from shifting when the vertical bar appears.
      */}
      <List
        sx={{
          flexGrow: 1,
          minHeight: 0,
          overflowY: 'auto',
          overflowX: 'hidden',
          scrollbarWidth: 'thin',
          scrollbarGutter: 'stable',
        }}
      >
        {items.map((item) =>
          item.children && item.children.length > 0 ? (
            <GroupEntry key={item.text} item={item} isCollapsed={isCollapsed} go={handleNavigation} />
          ) : (
            <LeafEntry key={item.text} item={item} isCollapsed={isCollapsed} go={handleNavigation} />
          )
        )}
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
    <Box component="nav" sx={{ width: { sm: drawerWidth }, flexShrink: { sm: 0 } }}>
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
        sx={(theme) => ({
          display: { xs: 'none', sm: 'block' },
          '& .MuiDrawer-paper': {
            boxSizing: 'border-box',
            width: drawerWidth,
            overflowX: 'hidden',
            transition: theme.transitions.create('width', {
              easing: theme.transitions.easing.sharp,
              duration: collapsed
                ? theme.transitions.duration.leavingScreen
                : theme.transitions.duration.enteringScreen,
            }),
          },
        })}
        open
      >
        {buildContent(collapsed, true)}
      </Drawer>
    </Box>
  );
}
