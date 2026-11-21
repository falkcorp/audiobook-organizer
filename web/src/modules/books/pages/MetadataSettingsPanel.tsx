// file: web/src/modules/books/pages/MetadataSettingsPanel.tsx
// version: 0.1.0
// guid: 6f8a8844-6c64-4f49-a551-d4f9833c2cf8
// last-edited: 2026-11-21

// The Metadata tab.

import { MetadataSettingsTab } from '../../../components/settings/MetadataSettingsTab';
import { useBooksSettings } from './booksSettingsContext';

export function MetadataSettingsPanel() {
  const {
    expandedSource,
    handleChange,
    handleCredentialChange,
    handleSourceReorder,
    handleSourceToggle,
    handleTestAIConnection,
    handleTestMetadataSource,
    openaiKeyError,
    openaiTestState,
    savedApiKeyMask,
    setExpandedSource,
    setSavedApiKeyMask,
    setSettings,
    settings,
    sourceTestStatus,
  } = useBooksSettings();

  return (
    <MetadataSettingsTab
      settings={settings}
      setSettings={setSettings}
      handleChange={handleChange}
      expandedSource={expandedSource}
      setExpandedSource={setExpandedSource}
      openaiTestState={openaiTestState}
      openaiKeyError={openaiKeyError}
      savedApiKeyMask={savedApiKeyMask}
      setSavedApiKeyMask={setSavedApiKeyMask}
      sourceTestStatus={sourceTestStatus}
      handleTestAIConnection={handleTestAIConnection}
      handleSourceToggle={handleSourceToggle}
      handleTestMetadataSource={handleTestMetadataSource}
      handleCredentialChange={handleCredentialChange}
      handleSourceReorder={handleSourceReorder}
    />
  );
}
