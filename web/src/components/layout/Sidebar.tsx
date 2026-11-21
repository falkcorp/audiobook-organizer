// file: web/src/components/layout/Sidebar.tsx
// version: 1.24.1
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-11-07
import { booksNavItems } from '../../modules/books/layout/nav';
import { SidebarFrame, type SidebarFrameProps } from './SidebarFrame';

/** The sidebar bound to the audiobook entries; F9-03 re-points importers to the shell and deletes this file. */
export function Sidebar(props: Omit<SidebarFrameProps, 'items'>) {
  return <SidebarFrame items={booksNavItems} {...props} />;
}
