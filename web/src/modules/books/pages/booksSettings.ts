// file: web/src/modules/books/pages/booksSettings.ts
// version: 0.1.0
// guid: 3384dce4-09da-4771-ab47-264cf2779458
// last-edited: 2026-11-21

// Books' settings contribution: the panels that edit the library's own configuration,
// as ids and labels matching the Settings tab slugs in the URL (?tab=<id>).

import type { ComponentType } from 'react';
import { LibrarySettingsPanel } from './LibrarySettingsPanel';
import { ITunesSettingsPanel } from './ITunesSettingsPanel';
import { MetadataSettingsPanel } from './MetadataSettingsPanel';
import { DedupSettingsPanel } from './DedupSettingsPanel';
import { PathsSettingsPanel } from './PathsSettingsPanel';
import { PerformanceSettingsPanel } from './PerformanceSettingsPanel';
import { BlockedHashesSettingsPanel } from './BlockedHashesSettingsPanel';
import { ToolsSettingsPanel } from './ToolsSettingsPanel';
import { SystemSettingsPanel } from './SystemSettingsPanel';

export interface BooksSettingsPanel {
  id: string;
  label: string;
  Component: ComponentType;
}

/** Keyed by tab slug. The binding interleaves these with the shell's own panels. */
export const booksSettingsPanels = {
  library: { id: 'library', label: 'Library', Component: LibrarySettingsPanel },
  itunes: { id: 'itunes', label: 'iTunes Import', Component: ITunesSettingsPanel },
  metadata: { id: 'metadata', label: 'Metadata', Component: MetadataSettingsPanel },
  dedup: { id: 'dedup', label: 'Dedup', Component: DedupSettingsPanel },
  paths: { id: 'paths', label: 'Paths', Component: PathsSettingsPanel },
  performance: { id: 'performance', label: 'Performance', Component: PerformanceSettingsPanel },
  security: { id: 'security', label: 'Security', Component: BlockedHashesSettingsPanel },
  tools: { id: 'tools', label: 'Tools', Component: ToolsSettingsPanel },
  system: { id: 'system', label: 'System Info', Component: SystemSettingsPanel },
} as const satisfies Record<string, BooksSettingsPanel>;
