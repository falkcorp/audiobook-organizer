// file: web/src/pages/Settings.tsx
// version: 1.68.3
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-11-09

// ABO's binding for the Settings page: the shell frame fed with the books panels and
// the shell's own panels, in the tab order the page has always had. F9-03 deletes this
// file when the shell takes over the route.

import { SettingsFrame, type SettingsFramePanel } from '../components/settings/SettingsFrame';
import PluginsTab from '../components/settings/PluginsTab';
import { APIKeysTab } from '../components/settings/APIKeysTab';
import { TempLoginTab } from '../components/settings/TempLoginTab';
import { BooksSettingsProvider } from '../modules/books/pages/BooksSettingsProvider';
import { BooksSettingsBanners } from '../modules/books/pages/BooksSettingsBanners';
import { BooksSettingsFooter } from '../modules/books/pages/BooksSettingsFooter';
import { booksSettingsPanels as books } from '../modules/books/pages/booksSettings';

const PANELS: readonly SettingsFramePanel[] = [
  books.library,
  books.itunes,
  books.metadata,
  books.dedup,
  books.paths,
  books.performance,
  books.security,
  { id: 'api-keys', label: 'API Keys', Component: APIKeysTab },
  { id: 'plugins', label: 'Plugins', Component: PluginsTab },
  books.tools,
  books.system,
  { id: 'temp-login', label: 'Temp Login', Component: TempLoginTab },
];

export function Settings() {
  return (
    <BooksSettingsProvider>
      <SettingsFrame
        panels={PANELS}
        banners={<BooksSettingsBanners />}
        footer={<BooksSettingsFooter />}
      />
    </BooksSettingsProvider>
  );
}
