// file: core/web/packages/ui-kit/src/components/index.ts
// version: 0.1.0
// guid: 8d2b6e41-5c03-4f97-a1e8-3b7c9d05f264
// last-edited: 2026-11-21

// ui-kit's presentational components (07 §7.11). They take their data, their
// transport and their storage keys as props: ui-kit imports neither
// @mediacore/api-client nor @mediacore/shell (§7.3.1).

export { AnnouncementBanner, type Announcement, type AnnouncementBannerProps } from './AnnouncementBanner';
export { BulkConfirmDialog, type BulkConfirmDialogProps } from './BulkConfirmDialog';
export {
  ColumnPicker,
  ResizableHeaderCell,
  useConfigurableTable,
  type ColumnDef,
  type ColumnPickerProps,
  type ResizableHeaderCellProps,
  type UseConfigurableTableOptions,
  type UseConfigurableTableResult,
} from './ConfigurableTable';
export { ErrorBoundary, type ErrorBoundaryProps } from './ErrorBoundary';
export { FilterTagBar, type FilterChipColor, type FilterTag, type FilterTagBarProps } from './FilterTagBar';
export { KeyboardShortcutsDialog, type KeyboardShortcutsDialogProps, type ShortcutHelpEntry } from './KeyboardShortcutsDialog';
export {
  ServerFileBrowser,
  type BrowseDiskInfo,
  type BrowseEntry,
  type BrowseResult,
  type ServerFileBrowserProps,
} from './ServerFileBrowser';
