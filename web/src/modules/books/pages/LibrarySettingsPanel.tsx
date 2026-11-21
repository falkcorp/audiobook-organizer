// file: web/src/modules/books/pages/LibrarySettingsPanel.tsx
// version: 0.1.0
// guid: e758961d-fcde-4265-b7e0-ba691edc4fc7
// last-edited: 2026-11-21

// The Library tab: the form fields plus the backup list.

import { SettingsGeneral } from '../../../components/SettingsGeneral';
import { useBooksSettings } from './booksSettingsContext';

export function LibrarySettingsPanel() {
  const {
    backupNotice,
    backups,
    backupsLoading,
    createBackupInProgress,
    excludePatternError,
    excludePatternInput,
    extensionsError,
    extensionsInput,
    handleAddExcludePattern,
    handleAddExtension,
    handleBrowseLibraryPath,
    handleChange,
    handleCreateBackup,
    handleRemoveExcludePattern,
    handleRemoveExtension,
    handleRequestDeleteBackup,
    handleRequestRestore,
    libraryPathError,
    namingPatternTokens,
    setExcludePatternInput,
    setExtensionsInput,
    setSettings,
    settings,
  } = useBooksSettings();

  return (
    <SettingsGeneral
      namingPatternTokens={namingPatternTokens}
      settings={settings}
      setSettings={setSettings}
      libraryPathError={libraryPathError}
      handleChange={handleChange}
      handleBrowseLibraryPath={handleBrowseLibraryPath}
      extensionsInput={extensionsInput}
      setExtensionsInput={setExtensionsInput}
      extensionsError={extensionsError}
      handleAddExtension={handleAddExtension}
      handleRemoveExtension={handleRemoveExtension}
      excludePatternInput={excludePatternInput}
      setExcludePatternInput={setExcludePatternInput}
      excludePatternError={excludePatternError}
      handleAddExcludePattern={handleAddExcludePattern}
      handleRemoveExcludePattern={handleRemoveExcludePattern}
      backupNotice={backupNotice}
      createBackupInProgress={createBackupInProgress}
      handleCreateBackup={handleCreateBackup}
      backupsLoading={backupsLoading}
      backups={backups}
      handleRequestRestore={handleRequestRestore}
      handleRequestDeleteBackup={handleRequestDeleteBackup}
    />
  );
}
