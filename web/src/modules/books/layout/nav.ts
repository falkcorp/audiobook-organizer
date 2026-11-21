// file: web/src/modules/books/layout/nav.ts
// version: 0.1.0
// guid: 7a3c5e91-2d68-4f04-9b1a-e8d0c6f2a753
// last-edited: 2026-11-21

// The audiobook sidebar entries: what Sidebar.tsx used to hold in its
// librarySubItems and menuItems arrays, in the same order and with the same
// paths. The shell's sidebar frame renders them; it names none of them.

import BugReportIcon from '@mui/icons-material/BugReport';
import BookmarksIcon from '@mui/icons-material/Bookmarks';
import CollectionsBookmarkIcon from '@mui/icons-material/CollectionsBookmark';
import DashboardIcon from '@mui/icons-material/Dashboard';
import FactCheckIcon from '@mui/icons-material/FactCheck';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import LibraryBooksIcon from '@mui/icons-material/LibraryBooks';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import MonitorIcon from '@mui/icons-material/Monitor';
import PeopleIcon from '@mui/icons-material/People';
import SettingsIcon from '@mui/icons-material/Settings';
import TimelineIcon from '@mui/icons-material/Timeline';
import WavesIcon from '@mui/icons-material/Waves';
import type { SidebarItem } from '../../../components/layout/SidebarFrame';
import { useReviewCount } from './useReviewCount';

const librarySubItems: readonly SidebarItem[] = [
  // path carries ?reset=1 so Library.tsx does a true full reset (page,
  // search, sort, filters, tags) instead of the state-preserving nav most
  // other sidebar links use; matchPath (query-free) drives the selected
  // highlight so it still lights up on a plain /library visit.
  //
  // matchSearch is the DECODED value of the `search` query param that marks
  // this item active. It cannot be derived from `path`: once Library.tsx
  // settles the URL, `search` is percent-encoded and `page=1` is appended, so
  // '/library?search=read_status:in_progress' never string-matches the live
  // location. Comparing the parsed param sidesteps both.
  {
    text: 'All Books',
    icon: LibraryBooksIcon,
    path: '/library?reset=1',
    matchPath: '/library',
    matchSearch: '',
  },
  {
    text: 'In Progress',
    icon: MenuBookIcon,
    path: '/library?search=read_status:in_progress',
    matchSearch: 'read_status:in_progress',
  },
  {
    text: 'Finished',
    icon: LibraryBooksIcon,
    path: '/library?search=read_status:finished',
    matchSearch: 'read_status:finished',
  },
  { text: 'Fingerprints', icon: WavesIcon, path: '/fingerprints' },
  { text: 'Series', icon: CollectionsBookmarkIcon, path: '/series' },
  { text: 'Authors', icon: PeopleIcon, path: '/authors' },
];

/** Dashboard, the Library group, then the rest, in sidebar order. */
export const booksNavItems: readonly SidebarItem[] = [
  { text: 'Dashboard', icon: DashboardIcon, path: '/dashboard' },
  { text: 'Library', icon: LibraryBooksIcon, path: '/library', children: librarySubItems },
  { text: 'File Browser', icon: FolderOpenIcon, path: '/files' },
  { text: 'Works', icon: MenuBookIcon, path: '/works' },
  { text: 'Playlists', icon: MenuBookIcon, path: '/playlists' },
  { text: 'Collections', icon: BookmarksIcon, path: '/collections' },
  { text: 'Activity', icon: TimelineIcon, path: '/activity' },
  { text: 'Review', icon: FactCheckIcon, path: '/review', useBadge: useReviewCount },
  { text: 'Diagnostics', icon: BugReportIcon, path: '/diagnostics' },
  { text: 'System', icon: MonitorIcon, path: '/system' },
  { text: 'Users', icon: PeopleIcon, path: '/users' },
  { text: 'Settings', icon: SettingsIcon, path: '/settings' },
];
