// file: web/src/modules/books/pages/PerformanceSettingsPanel.tsx
// version: 0.1.0
// guid: 8db40370-7907-437c-8853-cfbd333e50fa
// last-edited: 2026-11-21

// The Performance tab.

import { PerformanceSettingsTab } from '../../../components/settings/PerformanceSettingsTab';
import { useBooksSettings } from './booksSettingsContext';

export function PerformanceSettingsPanel() {
  const { handleChange, settings } = useBooksSettings();

  return <PerformanceSettingsTab settings={settings} handleChange={handleChange} />;
}
