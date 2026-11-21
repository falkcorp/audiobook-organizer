// file: web/src/modules/books/pages/useBooksSetupState.ts
// version: 0.1.0
// guid: c3cc45c0-8208-4239-adf7-525f5ea2a555
// last-edited: 2026-11-21

// F9-04 slice 5: books' first-run setup state (07 §7.11 "setup"): moved verbatim from the WelcomeWizard.

import { useState, useEffect } from 'react';
import * as api from '../../../services/api';
import { STORAGE_KEYS } from '../../../lib/storageKeys';
import { useToast } from '../../../components/toast/ToastProvider';
import { installRecommendedTools } from './installRecommendedTools';

/**
 * All of the first-run wizard's books state: the library path, the OpenAI key, the iTunes library,
 * the import folders, and the save that applies them. The steps read it through the context the
 * provider offers, so it survives moving between steps.
 */
export function useBooksSetupState({ open, onComplete }: { open: boolean; onComplete: () => void }) {
  const [libraryPath, setLibraryPath] = useState('');
  const [homePath, setHomePath] = useState('');
  const [libraryPathSelection, setLibraryPathSelection] = useState('');
  const [showLibraryBrowser, setShowLibraryBrowser] = useState(false);
  const [openaiKey, setOpenaiKey] = useState('');
  const [testingKey, setTestingKey] = useState(false);
  const [keyTestResult, setKeyTestResult] = useState<'success' | 'error' | null>(null);
  const [importFolders, setImportFolders] = useState<string[]>([]);
  const [importFolderSelection, setImportFolderSelection] = useState('');
  const [showImportBrowser, setShowImportBrowser] = useState(false);
  const [saving, setSaving] = useState(false);
  const [iTunesPath, setITunesPath] = useState('');
  const [iTunesPathSelection, setITunesPathSelection] = useState('');
  const [showITunesBrowser, setShowITunesBrowser] = useState(false);
  const [iTunesValidating, setITunesValidating] = useState(false);
  const [iTunesValidation, setITunesValidation] = useState<{
    valid: boolean;
    audiobook_count?: number;
    files_found?: number;
    error?: string;
  } | null>(null);
  const [iTunesEnabled, setITunesEnabled] = useState(false);
  const [iTunesImportMode, setITunesImportMode] = useState<'import' | 'organize'>('import');
  const [pathMappings, setPathMappings] = useState<api.PathMapping[]>([]);
  const [mappingTests, setMappingTests] = useState<
    Record<number, api.ITunesTestMappingResponse | null>
  >({});
  const [testingMapping, setTestingMapping] = useState<number | null>(null);
  // 'skip' is what the Tools step's Skip button sets: nothing is installed. 'custom' shows the tools
  // panel, which installs one tool at a time itself.
  const [toolInstallChoice, setToolInstallChoice] = useState<'recommended' | 'custom' | 'skip'>(
    'recommended'
  );
  const { toast } = useToast();

  useEffect(() => {
    if (!open) return;
    let cancelled = false;

    const loadHomePath = async () => {
      try {
        const path = await api.getHomeDirectory();
        if (cancelled) return;
        setHomePath(path);
        setLibraryPath((prev) => ((prev || '').trim() ? prev : path));
      } catch (_error) {
        if (cancelled) return;
        setHomePath((prev) => ((prev || '').trim() ? prev : '/'));
        setLibraryPath((prev) => ((prev || '').trim() ? prev : '/'));
      }
    };

    loadHomePath();

    return () => {
      cancelled = true;
    };
  }, [open]);

  const handleOpenLibraryBrowser = () => {
    setLibraryPathSelection(libraryPath || homePath || '/');
    setShowLibraryBrowser(true);
  };

  const handleLibraryPathSelect = (path: string) => {
    setLibraryPathSelection(path);
  };

  const handleConfirmLibraryPath = () => {
    const trimmed = (libraryPathSelection || '').trim();
    if (trimmed) {
      setLibraryPath(trimmed);
    }
    setShowLibraryBrowser(false);
  };

  const handleTestOpenAIKey = async () => {
    if (!(openaiKey || '').trim()) {
      setKeyTestResult('error');
      return;
    }

    setTestingKey(true);
    setKeyTestResult(null);

    try {
      // SEC-9: the backend makes the outbound OpenAI call, so the raw key never
      // appears in the browser's network log. The result shape is unchanged
      // from the caller's point of view.
      const result = await api.validateOpenAIKey(openaiKey);
      setKeyTestResult(result.valid ? 'success' : 'error');
    } catch (_error) {
      setKeyTestResult('error');
    } finally {
      setTestingKey(false);
    }
  };

  const handleOpenImportBrowser = () => {
    setImportFolderSelection(homePath || '/');
    setShowImportBrowser(true);
  };

  const handleImportFolderSelect = (path: string) => {
    setImportFolderSelection(path);
  };

  const handleConfirmImportFolder = () => {
    const trimmed = (importFolderSelection || '').trim();
    if (trimmed) {
      setImportFolders((prev) => (prev.includes(trimmed) ? prev : [...prev, trimmed]));
    }
    setShowImportBrowser(false);
  };

  const handleRemoveImportFolder = (index: number) => {
    setImportFolders(importFolders.filter((_, i) => i !== index));
  };

  const handleOpenITunesBrowser = () => {
    setITunesPathSelection(iTunesPath || homePath || '/');
    setShowITunesBrowser(true);
  };

  const handleConfirmITunesPath = async () => {
    const trimmed = (iTunesPathSelection || '').trim();
    if (!trimmed) return;
    setITunesPath(trimmed);
    setShowITunesBrowser(false);

    // Validate the iTunes library
    setITunesValidating(true);
    setITunesValidation(null);
    try {
      const activeMappings = pathMappings.filter((m) => m.from && m.to);
      const result = await api.validateITunesLibrary({
        library_path: trimmed,
        path_mappings: activeMappings.length > 0 ? activeMappings : undefined,
      });
      setITunesValidation({
        valid: true,
        audiobook_count: result.audiobook_count || result.audiobook_tracks,
        files_found: result.files_found,
      });
      // Auto-populate path mappings from detected prefixes
      if (result.path_prefixes?.length && pathMappings.length === 0) {
        setPathMappings(result.path_prefixes.map((p) => ({ from: p, to: '' })));
      }
      setITunesEnabled(true);
    } catch (error) {
      setITunesValidation({
        valid: false,
        error: error instanceof Error ? error.message : 'Validation failed',
      });
    } finally {
      setITunesValidating(false);
    }
  };

  const complete = async () => {
    setSaving(true);

    try {
      // Step 1: Save the setup in ONE request. The server lets an unauthenticated
      // first-run caller write exactly these keys, and only until setup_complete
      // is set: a key sent in a later request is refused once setup is complete,
      // so the OpenAI key and AI parsing travel with setup_complete, never after.
      const key = (openaiKey || '').trim();
      await api.updateConfig({
        root_dir: libraryPath,
        playlist_dir: `${libraryPath}/playlists`,
        ...(key ? { secrets: { openai_api_key: openaiKey }, enable_ai_parsing: true } : {}),
        setup_complete: true,
      });

      // Step 2: Add import folders
      for (const path of importFolders) {
        try {
          await api.addImportPath(path, path.split('/').pop() || path);
        } catch (error) {
          console.error(`Failed to add import folder ${path}:`, error);
        }
      }

      // Step 3: Trigger iTunes import if configured
      if (iTunesEnabled && iTunesPath) {
        // Persist iTunes settings so the Settings page can see them
        const activeMappings = pathMappings.filter((m) => m.from && m.to);
        try {
          localStorage.setItem(
            STORAGE_KEYS.ITUNES_IMPORT_SETTINGS,
            JSON.stringify({
              libraryPath: iTunesPath,
              importMode: iTunesImportMode,
              preserveLocation: iTunesImportMode === 'import',
              importPlaylists: true,
              skipDuplicates: true,
              pathMappings: activeMappings,
            })
          );
        } catch {
          // ignore localStorage errors
        }

        try {
          await api.importITunesLibrary({
            library_path: iTunesPath,
            import_mode: iTunesImportMode,
            preserve_location: iTunesImportMode === 'import',
            import_playlists: true,
            skip_duplicates: true,
            path_mappings: activeMappings,
          });
        } catch (error) {
          console.error('Failed to start iTunes import:', error);
        }
      }

      // Mark wizard as completed (store in localStorage for now)
      localStorage.setItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED, 'true');

      onComplete();

      // Last, and not awaited: the setup is already saved, and the install downloads for as long as
      // Ollama takes. It reports through toasts and never throws.
      if (toolInstallChoice === 'recommended') {
        void installRecommendedTools(toast);
      }
    } catch (error) {
      console.error('Failed to complete setup:', error);
      // Name the server's reason: a library root can now be refused (a 422 that
      // names the field, a 403 for a caller short of the admin role), and
      // "check the console" hides which.
      const reason = error instanceof Error && error.message ? `\n\n${error.message}` : '';
      alert(`Failed to save settings.${reason}`);
    } finally {
      setSaving(false);
    }
  };

  // Only the library path blocks the way forward; the other steps are optional.
  const canProceed = (stepId: string) =>
    stepId === 'library-path' ? (libraryPath || '').trim() !== '' : true;

  return {
    libraryPath,
    setLibraryPath,
    homePath,
    libraryPathSelection,
    showLibraryBrowser,
    setShowLibraryBrowser,
    openaiKey,
    setOpenaiKey,
    testingKey,
    keyTestResult,
    importFolders,
    importFolderSelection,
    showImportBrowser,
    setShowImportBrowser,
    saving,
    iTunesPath,
    setITunesPath,
    iTunesPathSelection,
    setITunesPathSelection,
    showITunesBrowser,
    setShowITunesBrowser,
    iTunesValidating,
    setITunesValidating,
    iTunesValidation,
    setITunesValidation,
    setITunesEnabled,
    iTunesImportMode,
    setITunesImportMode,
    pathMappings,
    setPathMappings,
    mappingTests,
    setMappingTests,
    testingMapping,
    setTestingMapping,
    toolInstallChoice,
    setToolInstallChoice,
    handleOpenLibraryBrowser,
    handleLibraryPathSelect,
    handleConfirmLibraryPath,
    handleTestOpenAIKey,
    handleOpenImportBrowser,
    handleImportFolderSelect,
    handleConfirmImportFolder,
    handleRemoveImportFolder,
    handleOpenITunesBrowser,
    handleConfirmITunesPath,
    complete,
    canProceed,
  };
}
