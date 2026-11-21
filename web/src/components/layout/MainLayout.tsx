// file: web/src/components/layout/MainLayout.tsx
// version: 1.5.1
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-08-19
import type { ReactNode } from 'react';
import { BooksBanners } from '../../modules/books/layout/banners';
import { booksNavItems } from '../../modules/books/layout/nav';
import { MainLayoutFrame } from './MainLayoutFrame';
import { TopBar } from './TopBar';

interface MainLayoutProps {
  children: ReactNode;
}

/** The layout bound to the books nav entries, top bar and banners; F9-03 re-points App.tsx to the shell and deletes this file. */
export function MainLayout({ children }: MainLayoutProps) {
  return (
    <MainLayoutFrame
      navItems={booksNavItems}
      topBar={(props) => <TopBar {...props} />}
      banners={<BooksBanners />}
    >
      {children}
    </MainLayoutFrame>
  );
}
