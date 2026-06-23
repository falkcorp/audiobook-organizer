// file: web/src/hooks/useMetadataSourceHandlers.ts
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-06-23

import type { Dispatch, SetStateAction } from 'react';
import * as api from '../services/api';
import type { SettingsState } from '../pages/Settings';

type SourceTestStatus = Record<
  string,
  { testing: boolean; result?: { success: boolean; message?: string; error?: string } }
>;

export interface UseMetadataSourceHandlersParams {
  settings: SettingsState;
  setSettings: Dispatch<SetStateAction<SettingsState>>;
  setSaved: Dispatch<SetStateAction<boolean>>;
  setSourceTestStatus: Dispatch<SetStateAction<SourceTestStatus>>;
}

export interface UseMetadataSourceHandlersReturn {
  handleSourceToggle: (sourceId: string) => void;
  handleTestMetadataSource: (sourceId: string) => Promise<void>;
  handleCredentialChange: (sourceId: string, field: string, value: string) => void;
  handleSourceReorder: (sourceId: string, direction: 'up' | 'down') => void;
}

export function useMetadataSourceHandlers(
  params: UseMetadataSourceHandlersParams
): UseMetadataSourceHandlersReturn {
  const { settings, setSettings, setSaved, setSourceTestStatus } = params;

  const handleSourceToggle = (sourceId: string) => {
    setSettings((prev) => ({
      ...prev,
      metadataSources: prev.metadataSources.map((source) =>
        source.id === sourceId
          ? { ...source, enabled: !source.enabled }
          : source
      ),
    }));
    setSaved(false);
  };

  const handleTestMetadataSource = async (sourceId: string) => {
    const source = settings.metadataSources.find((s) => s.id === sourceId);
    const apiKey = source?.credentials?.apiKey || '';
    if (!apiKey) {
      setSourceTestStatus((prev) => ({
        ...prev,
        [sourceId]: { testing: false, result: { success: false, error: 'No API key entered' } },
      }));
      return;
    }
    setSourceTestStatus((prev) => ({
      ...prev,
      [sourceId]: { testing: true },
    }));
    try {
      const result = await api.testMetadataSource(sourceId, apiKey);
      setSourceTestStatus((prev) => ({
        ...prev,
        [sourceId]: { testing: false, result },
      }));
    } catch (err) {
      setSourceTestStatus((prev) => ({
        ...prev,
        [sourceId]: { testing: false, result: { success: false, error: String(err) } },
      }));
    }
  };

  const handleCredentialChange = (
    sourceId: string,
    field: string,
    value: string
  ) => {
    setSettings((prev) => ({
      ...prev,
      metadataSources: prev.metadataSources.map((source) =>
        source.id === sourceId
          ? {
              ...source,
              credentials: { ...source.credentials, [field]: value },
            }
          : source
      ),
    }));
    setSaved(false);
  };

  const handleSourceReorder = (sourceId: string, direction: 'up' | 'down') => {
    setSettings((prev) => {
      const sources = [...prev.metadataSources];
      const index = sources.findIndex((s) => s.id === sourceId);
      if (index === -1) return prev;

      const targetIndex = direction === 'up' ? index - 1 : index + 1;
      if (targetIndex < 0 || targetIndex >= sources.length) return prev;

      const temp = sources[index].priority;
      sources[index] = {
        ...sources[index],
        priority: sources[targetIndex].priority,
      };
      sources[targetIndex] = { ...sources[targetIndex], priority: temp };
      sources.sort((a, b) => a.priority - b.priority);

      return { ...prev, metadataSources: sources };
    });
    setSaved(false);
  };

  return {
    handleSourceToggle,
    handleTestMetadataSource,
    handleCredentialChange,
    handleSourceReorder,
  };
}
