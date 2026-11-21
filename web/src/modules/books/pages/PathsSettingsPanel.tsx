// file: web/src/modules/books/pages/PathsSettingsPanel.tsx
// version: 0.1.0
// guid: 2c99af5a-33eb-4a68-ba33-f8b4ac68ea6d
// last-edited: 2026-11-21

// The Paths tab: the library path and the import folders.

import { PathsSettingsTab } from '../../../components/settings/PathsSettingsTab';
import { useBooksSettings } from './booksSettingsContext';

export function PathsSettingsPanel() {
  const {
    envLocked,
    handleBrowseLibraryPath,
    handleChange,
    handleRemoveImportFolder,
    handleRequestCancelScan,
    handleScanImportFolder,
    handleViewScanErrors,
    importPaths,
    libraryPathError,
    scanStatuses,
    setAddFolderDialogOpen,
    setSettings,
    settingLocks,
    settings,
  } = useBooksSettings();

  return (
    <PathsSettingsTab
      envLocked={envLocked}
      settingLocks={settingLocks}
      settings={settings}
      setSettings={setSettings}
      libraryPathError={libraryPathError}
      handleChange={handleChange}
      handleBrowseLibraryPath={handleBrowseLibraryPath}
      importPaths={importPaths}
      scanStatuses={scanStatuses}
      handleViewScanErrors={handleViewScanErrors}
      handleRequestCancelScan={handleRequestCancelScan}
      handleScanImportFolder={handleScanImportFolder}
      handleRemoveImportFolder={handleRemoveImportFolder}
      setAddFolderDialogOpen={setAddFolderDialogOpen}
    />
  );
}
