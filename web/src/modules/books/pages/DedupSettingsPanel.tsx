// file: web/src/modules/books/pages/DedupSettingsPanel.tsx
// version: 0.1.0
// guid: 605d86a7-9933-410c-96d8-c2ef2468af6f
// last-edited: 2026-11-21

// The Dedup tab: dedup, embedding, AI backend, scoring and scheduled-task sections.

import { Box } from '@mui/material';
import { EmbeddingSettingsSection } from '../../../components/settings/EmbeddingSettingsSection';
import { AIBackendsSection } from '../../../components/settings/AIBackendsSection';
import { DedupSettingsSection } from '../../../components/settings/DedupSettingsSection';
import { MetadataScoringSection } from '../../../components/settings/MetadataScoringSection';
import { ScheduledTasksSection } from '../../../components/settings/ScheduledTasksSection';
import { useBooksSettings } from './booksSettingsContext';

export function DedupSettingsPanel() {
  const {
    acoustidKeyMask,
    aiBackendConfig,
    dedupConfig,
    embeddingConfig,
    handleAIBackendChange,
    handleDedupChange,
    handleEmbeddingChange,
    handleMetadataScoringChange,
    handleScheduledChange,
    metadataScoringConfig,
    scheduledConfig,
  } = useBooksSettings();

  return (
    <>
      <DedupSettingsSection
        config={dedupConfig}
        onChange={handleDedupChange}
        acoustidKeyMask={acoustidKeyMask}
      />
      <Box sx={{ mt: 4 }}>
        <EmbeddingSettingsSection config={embeddingConfig} onChange={handleEmbeddingChange} />
      </Box>
      <Box sx={{ mt: 4 }}>
        <AIBackendsSection config={aiBackendConfig} onChange={handleAIBackendChange} />
      </Box>
      <Box sx={{ mt: 4 }}>
        <MetadataScoringSection
          config={metadataScoringConfig}
          onChange={handleMetadataScoringChange}
        />
      </Box>
      {scheduledConfig && (
        <Box sx={{ mt: 4 }}>
          <ScheduledTasksSection config={scheduledConfig} onChange={handleScheduledChange} />
        </Box>
      )}
    </>
  );
}
