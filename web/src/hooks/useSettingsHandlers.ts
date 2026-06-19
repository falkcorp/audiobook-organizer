// file: web/src/hooks/useSettingsHandlers.ts
// version: 1.0.0
// guid: b8c9d0e1-f2a3-4567-bcde-678901234567
// last-edited: 2026-06-19

import { ChangeEvent } from 'react';
import { NavigateFunction } from 'react-router-dom';
import * as api from '../services/api';

// ---------------------------------------------------------------------------
// Local types (re-declared here so the hook is self-contained)
// ---------------------------------------------------------------------------

export interface ScanStatus {
  status: 'scanning' | 'complete' | 'error' | 'cancelled';
  scanned: number;
  total: number;
  operationId?: string;
  errors?: string[];
}

export interface ScanErrorTarget {
  path: string;
  errors: string[];
}

export interface SettingsState {
  libraryPath: string;
  organizationStrategy: string;
  scanOnStartup: boolean;
  autoOrganize: boolean;
  folderNamingPattern: string;
  fileNamingPattern: string;
  createBackups: boolean;
  supportedExtensions: string[];
  excludePatterns: string[];
  enableDiskQuota: boolean;
  diskQuotaPercent: number;
  enableUserQuotas: boolean;
  defaultUserQuotaGB: number;
  autoFetchMetadata: boolean;
  enableAIParsing: boolean;
  metadataLLMScoringEnabled: boolean;
  openaiApiKey: string;
  metadataSources: Array<{
    id: string;
    name: string;
    enabled: boolean;
    priority: number;
    requiresAuth: boolean;
    credentials: Record<string, string>;
  }>;
  language: string;
  concurrentScans: number;
  memoryLimitType: string;
  cacheSize: number;
  cacheInvalidateOnBookUpdate: boolean;
  metadataFetchCacheTTLDays: number;
  memoryLimitPercent: number;
  memoryLimitMB: number;
  purgeSoftDeletedAfterDays: number;
  purgeSoftDeletedDeleteFiles: boolean;
  logLevel: string;
  logFormat: string;
  enableJsonLogging: boolean;
  autoUpdateEnabled: boolean;
  autoUpdateChannel: string;
  autoUpdateCheckMinutes: number;
  autoUpdateWindowStart: number;
  autoUpdateWindowEnd: number;
  maintenanceWindowEnabled: boolean;
  maintenanceWindowStart: number;
  maintenanceWindowEnd: number;
  pathFormat: string;
  segmentTitleFormat: string;
  autoRenameOnApply: boolean;
  autoWriteTagsOnApply: boolean;
  verifyAfterWrite: boolean;
  protectedPaths: string;
}

interface Blocker {
  state: 'idle' | 'blocked';
  proceed: (() => void) | null;
  reset: (() => void) | null;
}

// ---------------------------------------------------------------------------
// Hook parameter interface
// ---------------------------------------------------------------------------

export interface UseSettingsHandlersParams {
  settings: SettingsState;
  setSettings: React.Dispatch<React.SetStateAction<SettingsState>>;
  setSaved: React.Dispatch<React.SetStateAction<boolean>>;
  setSavedApiKeyMask: React.Dispatch<React.SetStateAction<string>>;
  setConfigLoaded: React.Dispatch<React.SetStateAction<boolean>>;
  setDedupConfig: React.Dispatch<React.SetStateAction<api.DedupConfig>>;
  setEmbeddingConfig: React.Dispatch<React.SetStateAction<api.EmbeddingConfig>>;
  setMetadataScoringConfig: React.Dispatch<React.SetStateAction<api.MetadataScoringConfig>>;
  setMaintenanceConfig: React.Dispatch<React.SetStateAction<api.MaintenanceConfig>>;
  setScheduledConfig: React.Dispatch<React.SetStateAction<api.ScheduledTasksConfig | null>>;
  setToolsConfig: React.Dispatch<React.SetStateAction<api.ToolsConfig>>;
  setLibraryPathError: React.Dispatch<React.SetStateAction<string | null>>;
  setOpenaiKeyError: React.Dispatch<React.SetStateAction<string | null>>;
  setExtensionsError: React.Dispatch<React.SetStateAction<string | null>>;
  setExcludePatternError: React.Dispatch<React.SetStateAction<string | null>>;
  setImportFolders: React.Dispatch<React.SetStateAction<api.ImportPath[]>>;
  setScanStatuses: React.Dispatch<React.SetStateAction<Record<number, ScanStatus>>>;
  setCancelScanTarget: React.Dispatch<React.SetStateAction<api.ImportPath | null>>;
  setScanErrorTarget: React.Dispatch<React.SetStateAction<ScanErrorTarget | null>>;
  setBackups: React.Dispatch<React.SetStateAction<api.BackupInfo[]>>;
  setBackupNotice: React.Dispatch<React.SetStateAction<{ severity: 'success' | 'error' | 'info'; message: string } | null>>;
  setBackupsLoading: React.Dispatch<React.SetStateAction<boolean>>;
  setRestoreTarget: React.Dispatch<React.SetStateAction<api.BackupInfo | null>>;
  setRestoreDialogOpen: React.Dispatch<React.SetStateAction<boolean>>;
  setRestoreInProgress: React.Dispatch<React.SetStateAction<boolean>>;
  setDeleteBackupTarget: React.Dispatch<React.SetStateAction<api.BackupInfo | null>>;
  setDeleteBackupInProgress: React.Dispatch<React.SetStateAction<boolean>>;
  setCreateBackupInProgress: React.Dispatch<React.SetStateAction<boolean>>;
  setOpenaiTestState: React.Dispatch<React.SetStateAction<{ status: 'idle' | 'loading' | 'success' | 'error'; message?: string; model?: string }>>;
  setSavedSnapshot: React.Dispatch<React.SetStateAction<string>>;
  setSourceTestStatus: React.Dispatch<React.SetStateAction<Record<string, { testing: boolean; result?: { success: boolean; message?: string; error?: string } }>>>;
  setExpandedSource: React.Dispatch<React.SetStateAction<string | null>>;
  setBrowserOpen: React.Dispatch<React.SetStateAction<boolean>>;
  setSelectedPath: React.Dispatch<React.SetStateAction<string | null>>;
  setAddFolderDialogOpen: React.Dispatch<React.SetStateAction<boolean>>;
  setNewFolderPath: React.Dispatch<React.SetStateAction<string>>;
  setShowFolderBrowser: React.Dispatch<React.SetStateAction<boolean>>;
  setImportDialogOpen: React.Dispatch<React.SetStateAction<boolean>>;
  setImportPayload: React.Dispatch<React.SetStateAction<Partial<api.Config> | null>>;
  setImportFileName: React.Dispatch<React.SetStateAction<string>>;
  setImportNotice: React.Dispatch<React.SetStateAction<string | null>>;
  setExportInProgress: React.Dispatch<React.SetStateAction<boolean>>;
  setImportInProgress: React.Dispatch<React.SetStateAction<boolean>>;
  setExtensionsInput: React.Dispatch<React.SetStateAction<string>>;
  setExcludePatternInput: React.Dispatch<React.SetStateAction<string>>;
  savedApiKeyMask: string;
  configLoaded: boolean;
  navigate: NavigateFunction;
  scanIntervalsRef: React.MutableRefObject<Record<number, number>>;
  isUnmountedRef: React.MutableRefObject<boolean>;
  timeoutRef: React.MutableRefObject<ReturnType<typeof setTimeout> | null>;
  restoreTarget: api.BackupInfo | null;
  restoreVerify: boolean;
  deleteBackupTarget: api.BackupInfo | null;
  cancelScanTarget: api.ImportPath | null;
  scanStatuses: Record<number, ScanStatus>;
  importPayload: Partial<api.Config> | null;
  importFileName: string;
  savedSettings: SettingsState | null;
  extensionsInput: string;
  excludePatternInput: string;
  newFolderPath: string;
  selectedPath: string | null;
  blocker: Blocker;
  dedupConfig: api.DedupConfig;
  embeddingConfig: api.EmbeddingConfig;
  metadataScoringConfig: api.MetadataScoringConfig;
  maintenanceConfig: api.MaintenanceConfig;
  scheduledConfig: api.ScheduledTasksConfig | null;
  toolsConfig: api.ToolsConfig;
  importInputRef: React.MutableRefObject<HTMLInputElement | null>;
  loadConfig: () => Promise<void>;
  initialSettings: SettingsState;
}

// ---------------------------------------------------------------------------
// Hook return type
// ---------------------------------------------------------------------------

export interface UseSettingsHandlersReturn {
  normalizeExtension: (value: string) => string;
  isValidOpenAIKey: (value: string) => boolean;
  handleChange: (field: string, value: string | boolean | number | string[]) => void;
  handleDedupChange: (patch: Partial<api.DedupConfig>) => void;
  handleEmbeddingChange: (patch: Partial<api.EmbeddingConfig>) => void;
  handleMetadataScoringChange: (patch: Partial<api.MetadataScoringConfig>) => void;
  handleMaintenanceChange: (patch: Partial<api.MaintenanceConfig>) => void;
  handleScheduledChange: (patch: Partial<api.ScheduledTasksConfig>) => void;
  handleToolsChange: (patch: Partial<api.ToolsConfig>) => void;
  handleBrowseLibraryPath: () => void;
  handleBrowserSelect: (path: string, isDir: boolean) => void;
  handleBrowserConfirm: () => void;
  handleBrowserCancel: () => void;
  loadImportFolders: () => Promise<void>;
  handleAddImportFolder: () => Promise<void>;
  handleRemoveImportFolder: (id: number) => Promise<void>;
  handleScanImportFolder: (folder: api.ImportPath) => Promise<void>;
  handleRequestCancelScan: (folder: api.ImportPath) => void;
  handleConfirmCancelScan: () => Promise<void>;
  handleViewScanErrors: (folder: api.ImportPath, status: ScanStatus) => void;
  loadBackups: () => Promise<void>;
  handleCreateBackup: () => Promise<void>;
  handleRequestRestore: (backup: api.BackupInfo) => void;
  handleConfirmRestore: () => Promise<void>;
  handleRequestDeleteBackup: (backup: api.BackupInfo) => void;
  handleConfirmDeleteBackup: () => Promise<void>;
  handleFolderBrowserSelect: (path: string, isDir: boolean) => void;
  handleSourceToggle: (sourceId: string) => void;
  handleTestMetadataSource: (sourceId: string) => Promise<void>;
  handleCredentialChange: (sourceId: string, field: string, value: string) => void;
  handleSourceReorder: (sourceId: string, direction: 'up' | 'down') => void;
  handleSave: () => Promise<boolean>;
  handleReset: () => void;
  handleAddExtension: () => void;
  handleRemoveExtension: (extension: string) => void;
  handleAddExcludePattern: () => void;
  handleRemoveExcludePattern: (pattern: string) => void;
  handleTestAIConnection: () => Promise<void>;
  handleExportSettings: () => Promise<void>;
  handleImportClick: () => void;
  handleImportFileChange: (event: ChangeEvent<HTMLInputElement>) => Promise<void>;
  handleConfirmImport: () => Promise<void>;
  handleSaveAndNavigate: () => Promise<void>;
  handleDiscardNavigation: () => void;
  handleCancelNavigation: () => void;
}

// ---------------------------------------------------------------------------
// Hook implementation
// ---------------------------------------------------------------------------

export function useSettingsHandlers(params: UseSettingsHandlersParams): UseSettingsHandlersReturn {
  const {
    settings,
    setSettings,
    setSaved,
    setSavedApiKeyMask,
    setConfigLoaded: _setConfigLoaded,
    setDedupConfig,
    setEmbeddingConfig,
    setMetadataScoringConfig,
    setMaintenanceConfig,
    setScheduledConfig,
    setToolsConfig,
    setLibraryPathError,
    setOpenaiKeyError,
    setExtensionsError,
    setExcludePatternError,
    setImportFolders,
    setScanStatuses,
    setCancelScanTarget,
    setScanErrorTarget,
    setBackups,
    setBackupNotice,
    setBackupsLoading,
    setRestoreTarget,
    setRestoreDialogOpen,
    setRestoreInProgress,
    setDeleteBackupTarget,
    setDeleteBackupInProgress,
    setCreateBackupInProgress,
    setOpenaiTestState,
    setSavedSnapshot,
    setSourceTestStatus,
    setExpandedSource: _setExpandedSource,
    setBrowserOpen,
    setSelectedPath,
    setAddFolderDialogOpen,
    setNewFolderPath,
    setShowFolderBrowser,
    setImportDialogOpen,
    setImportPayload,
    setImportFileName,
    setImportNotice,
    setExportInProgress,
    setImportInProgress,
    setExtensionsInput,
    setExcludePatternInput,
    savedApiKeyMask,
    configLoaded,
    navigate,
    scanIntervalsRef,
    isUnmountedRef,
    timeoutRef,
    restoreTarget,
    restoreVerify,
    deleteBackupTarget,
    cancelScanTarget,
    scanStatuses,
    importPayload,
    savedSettings,
    extensionsInput,
    excludePatternInput,
    newFolderPath,
    selectedPath,
    blocker,
    dedupConfig,
    embeddingConfig,
    metadataScoringConfig,
    maintenanceConfig,
    scheduledConfig,
    toolsConfig,
    importInputRef,
    loadConfig,
    initialSettings,
  } = params;

  // -------------------------------------------------------------------------
  // Utility
  // -------------------------------------------------------------------------

  const normalizeExtension = (value: string): string => {
    const trimmed = value.trim();
    if (!trimmed) return '';
    const withDot = trimmed.startsWith('.') ? trimmed : `.${trimmed}`;
    return withDot.toLowerCase();
  };

  const isValidOpenAIKey = (value: string): boolean => {
    if (!value) return true;
    return /^sk-[A-Za-z0-9_-]{8,}$/.test(value);
  };

  // -------------------------------------------------------------------------
  // Settings change handlers
  // -------------------------------------------------------------------------

  const handleChange = (
    field: string,
    value: string | boolean | number | string[]
  ) => {
    setSettings((prev) => ({ ...prev, [field]: value }));
    setSaved(false);
    if (field === 'libraryPath') {
      setLibraryPathError(null);
    }
    if (field === 'openaiApiKey') {
      setOpenaiKeyError(null);
      setOpenaiTestState({ status: 'idle' });
    }
  };

  const handleDedupChange = (patch: Partial<api.DedupConfig>) => {
    setDedupConfig((prev) => ({ ...prev, ...patch }));
    setSaved(false);
  };

  const handleEmbeddingChange = (patch: Partial<api.EmbeddingConfig>) => {
    setEmbeddingConfig((prev) => ({ ...prev, ...patch }));
    setSaved(false);
  };

  const handleMetadataScoringChange = (patch: Partial<api.MetadataScoringConfig>) => {
    setMetadataScoringConfig((prev) => ({ ...prev, ...patch }));
    setSaved(false);
  };

  const handleMaintenanceChange = (patch: Partial<api.MaintenanceConfig>) => {
    setMaintenanceConfig((prev) => ({ ...prev, ...patch }));
    setSaved(false);
  };

  const handleScheduledChange = (patch: Partial<api.ScheduledTasksConfig>) => {
    setScheduledConfig((prev) => (prev ? { ...prev, ...patch } : null));
    setSaved(false);
  };

  const handleToolsChange = (patch: Partial<api.ToolsConfig>) => {
    setToolsConfig((prev) => ({ ...prev, ...patch }));
    setSaved(false);
  };

  // -------------------------------------------------------------------------
  // Library path browser
  // -------------------------------------------------------------------------

  const handleBrowseLibraryPath = () => {
    setSelectedPath(settings.libraryPath);
    setBrowserOpen(true);
  };

  const handleBrowserSelect = (path: string, isDir: boolean) => {
    if (isDir) {
      setSelectedPath(path);
    }
  };

  const handleBrowserConfirm = () => {
    if (selectedPath) {
      handleChange('libraryPath', selectedPath);
    }
    setBrowserOpen(false);
  };

  const handleBrowserCancel = () => {
    setBrowserOpen(false);
    setSelectedPath(null);
  };

  // -------------------------------------------------------------------------
  // Import folder management
  // -------------------------------------------------------------------------

  const loadImportFolders = async () => {
    try {
      const folders = await api.getImportPaths();
      setImportFolders(folders);
    } catch (error) {
      console.error('Failed to load import folders:', error);
    }
  };

  const handleAddImportFolder = async () => {
    if (!newFolderPath.trim()) return;
    try {
      const folder = await api.addImportPath(
        newFolderPath,
        newFolderPath.split('/').pop() || 'Import Folder'
      );
      setImportFolders((prev) => [...prev, folder]);
      setNewFolderPath('');
      setShowFolderBrowser(false);
      setAddFolderDialogOpen(false);
    } catch (error) {
      console.error('Failed to add import folder:', error);
    }
  };

  const handleRemoveImportFolder = async (id: number) => {
    try {
      await api.removeImportPath(id);
      setImportFolders((prev) => prev.filter((f) => f.id !== id));
    } catch (error) {
      console.error('Failed to remove import folder:', error);
    }
  };

  const handleScanImportFolder = async (folder: api.ImportPath) => {
    setScanStatuses((prev) => ({
      ...prev,
      [folder.id]: {
        status: 'scanning',
        scanned: 0,
        total: prev[folder.id]?.total || 0,
      },
    }));

    let total = 50;
    let errors: string[] = [];
    let operationId: string | undefined;

    try {
      const response = await api.startScan(folder.path);
      if (typeof response.total === 'number') {
        total = response.total;
      }
      if (Array.isArray(response.errors)) {
        errors = response.errors;
      }
      operationId = response.id;
    } catch (error) {
      console.error('Failed to scan import folder:', error);
      const message =
        error instanceof Error ? error.message : 'Scan failed.';
      setScanStatuses((prev) => ({
        ...prev,
        [folder.id]: {
          status: 'error',
          scanned: 0,
          total: 0,
          errors: [message],
        },
      }));
      return;
    }

    setScanStatuses((prev) => ({
      ...prev,
      [folder.id]: {
        status: 'scanning',
        scanned: 0,
        total,
        operationId,
        errors,
      },
    }));

    let scanned = 0;
    const increment = Math.max(1, Math.ceil(total / 10));
    // Clear any existing interval for this folder
    if (scanIntervalsRef.current[folder.id]) {
      window.clearInterval(scanIntervalsRef.current[folder.id]);
    }
    const interval = window.setInterval(() => {
      scanned += increment;
      setScanStatuses((prev) => ({
        ...prev,
        [folder.id]: {
          status: scanned >= total ? 'complete' : 'scanning',
          scanned: Math.min(scanned, total),
          total,
          operationId,
          errors,
        },
      }));
      if (scanned >= total) {
        window.clearInterval(interval);
        delete scanIntervalsRef.current[folder.id];
      }
    }, 300);

    scanIntervalsRef.current[folder.id] = interval;
  };

  const handleRequestCancelScan = (folder: api.ImportPath) => {
    setCancelScanTarget(folder);
  };

  const handleConfirmCancelScan = async () => {
    if (!cancelScanTarget) return;
    const target = cancelScanTarget;
    setCancelScanTarget(null);
    const status = scanStatuses[target.id];
    if (!status) return;
    const interval = scanIntervalsRef.current[target.id];
    if (interval) {
      window.clearInterval(interval);
      delete scanIntervalsRef.current[target.id];
    }
    if (status.operationId) {
      try {
        await api.cancelOperation(status.operationId);
      } catch (error) {
        console.error('Failed to cancel scan operation:', error);
      }
    }
    setScanStatuses((prev) => ({
      ...prev,
      [target.id]: {
        ...status,
        status: 'cancelled',
      },
    }));
  };

  const handleViewScanErrors = (
    folder: api.ImportPath,
    status: ScanStatus
  ) => {
    if (!status.errors?.length) return;
    setScanErrorTarget({
      path: folder.path,
      errors: status.errors,
    });
  };

  // -------------------------------------------------------------------------
  // Backup management
  // -------------------------------------------------------------------------

  const loadBackups = async () => {
    setBackupsLoading(true);
    try {
      const data = await api.listBackups();
      const sorted = [...(data.backups || [])].sort(
        (a, b) =>
          new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      );
      setBackups(sorted);
    } catch (error) {
      console.error('Failed to load backups:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to load backups.',
      });
    } finally {
      setBackupsLoading(false);
    }
  };

  const handleCreateBackup = async () => {
    setCreateBackupInProgress(true);
    setBackupNotice(null);
    try {
      await api.createBackup();
      setBackupNotice({
        severity: 'success',
        message: 'Backup created successfully.',
      });
      await loadBackups();
    } catch (error) {
      console.error('Failed to create backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to create backup.',
      });
    } finally {
      setCreateBackupInProgress(false);
    }
  };

  const handleRequestRestore = (backup: api.BackupInfo) => {
    setRestoreTarget(backup);
    setRestoreDialogOpen(true);
  };

  const handleConfirmRestore = async () => {
    if (!restoreTarget) return;
    setRestoreInProgress(true);
    setBackupNotice(null);
    try {
      await api.restoreBackup(restoreTarget.filename, restoreVerify);
      setBackupNotice({
        severity: 'success',
        message: 'Backup restored successfully.',
      });
      setRestoreDialogOpen(false);
      window.location.reload();
    } catch (error) {
      console.error('Failed to restore backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Backup file is corrupt.',
      });
    } finally {
      setRestoreInProgress(false);
    }
  };

  const handleRequestDeleteBackup = (backup: api.BackupInfo) => {
    setDeleteBackupTarget(backup);
  };

  const handleConfirmDeleteBackup = async () => {
    if (!deleteBackupTarget) return;
    setDeleteBackupInProgress(true);
    setBackupNotice(null);
    try {
      await api.deleteBackup(deleteBackupTarget.filename);
      setBackupNotice({
        severity: 'success',
        message: 'Backup deleted successfully.',
      });
      setDeleteBackupTarget(null);
      await loadBackups();
    } catch (error) {
      console.error('Failed to delete backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to delete backup.',
      });
    } finally {
      setDeleteBackupInProgress(false);
    }
  };

  // -------------------------------------------------------------------------
  // Folder browser for import path dialog
  // -------------------------------------------------------------------------

  const handleFolderBrowserSelect = (path: string, isDir: boolean) => {
    if (isDir) {
      setNewFolderPath(path);
    }
  };

  // -------------------------------------------------------------------------
  // Metadata source management
  // -------------------------------------------------------------------------

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

      // Swap priorities
      const temp = sources[index].priority;
      sources[index] = {
        ...sources[index],
        priority: sources[targetIndex].priority,
      };
      sources[targetIndex] = { ...sources[targetIndex], priority: temp };

      // Sort by priority
      sources.sort((a, b) => a.priority - b.priority);

      return { ...prev, metadataSources: sources };
    });
    setSaved(false);
  };

  // -------------------------------------------------------------------------
  // Save / reset
  // -------------------------------------------------------------------------

  const handleSave = async (): Promise<boolean> => {
    if (!configLoaded) {
      console.warn('[Settings] Save blocked — config not yet loaded');
      return false;
    }
    setLibraryPathError(null);
    setOpenaiKeyError(null);
    setExtensionsError(null);
    setExcludePatternError(null);

    const libraryPath = settings.libraryPath.trim();
    if (!libraryPath) {
      setLibraryPathError('Library path is required.');
      return false;
    }
    if (savedSettings && savedSettings.libraryPath !== libraryPath) {
      try {
        await api.browseFilesystem(libraryPath);
      } catch (error) {
        console.error('Library path validation failed:', error);
        setLibraryPathError('Directory does not exist.');
        return false;
      }
    }
    if (settings.supportedExtensions.length === 0) {
      setExtensionsError('Add at least one extension.');
      return false;
    }
    if (!isValidOpenAIKey(settings.openaiApiKey)) {
      setOpenaiKeyError('Invalid API key format.');
      return false;
    }

    try {
      const updates: Partial<api.Config> = {
        root_dir: libraryPath,
        playlist_dir: `${libraryPath}/playlists`,
        organization_strategy: settings.organizationStrategy,
        scan_on_startup: settings.scanOnStartup,
        auto_organize: settings.autoOrganize,
        folder_naming_pattern: settings.folderNamingPattern,
        file_naming_pattern: settings.fileNamingPattern,
        create_backups: settings.createBackups,
        supported_extensions: settings.supportedExtensions,
        exclude_patterns: settings.excludePatterns,
        enable_disk_quota: settings.enableDiskQuota,
        disk_quota_percent: settings.diskQuotaPercent,
        enable_user_quotas: settings.enableUserQuotas,
        default_user_quota_gb: settings.defaultUserQuotaGB,
        auto_fetch_metadata: settings.autoFetchMetadata,
        enable_ai_parsing: settings.enableAIParsing,
        metadata_llm_scoring_enabled: settings.metadataLLMScoringEnabled,
        ...(settings.openaiApiKey ? { openai_api_key: settings.openaiApiKey } : {}),
        metadata_sources: settings.metadataSources.map((source) => ({
          id: source.id,
          name: source.name,
          enabled: source.enabled,
          priority: source.priority,
          requires_auth: source.requiresAuth,
          credentials: source.credentials as { [key: string]: string },
        })),
        language: settings.language,
        concurrent_scans: settings.concurrentScans,
        memory_limit_type: settings.memoryLimitType,
        cache_size: settings.cacheSize,
        cache_invalidate_on_book_update: settings.cacheInvalidateOnBookUpdate,
        metadata_fetch_cache_ttl_days: settings.metadataFetchCacheTTLDays,
        memory_limit_percent: settings.memoryLimitPercent,
        memory_limit_mb: settings.memoryLimitMB,
        purge_soft_deleted_after_days: settings.purgeSoftDeletedAfterDays,
        purge_soft_deleted_delete_files: settings.purgeSoftDeletedDeleteFiles,
        log_level: settings.logLevel,
        log_format: settings.logFormat,
        enable_json_logging: settings.enableJsonLogging,
        auto_update_enabled: settings.autoUpdateEnabled,
        auto_update_channel: settings.autoUpdateChannel,
        auto_update_check_minutes: settings.autoUpdateCheckMinutes,
        auto_update_window_start: settings.autoUpdateWindowStart,
        auto_update_window_end: settings.autoUpdateWindowEnd,
        auto_update: {
          enabled: settings.autoUpdateEnabled,
          channel: settings.autoUpdateChannel,
          check_minutes: settings.autoUpdateCheckMinutes,
          window_start: settings.autoUpdateWindowStart,
          window_end: settings.autoUpdateWindowEnd,
        },
        maintenance_window_enabled: maintenanceConfig.enabled,
        maintenance_window_start: maintenanceConfig.window_start,
        maintenance_window_end: maintenanceConfig.window_end,
        maintenance: maintenanceConfig,
        embedding: embeddingConfig,
        dedup: dedupConfig,
        metadata_scoring: metadataScoringConfig,
        ...(scheduledConfig ? { scheduled: scheduledConfig } : {}),
        tools: toolsConfig,
        path_format: settings.pathFormat,
        segment_title_format: settings.segmentTitleFormat,
        auto_rename_on_apply: settings.autoRenameOnApply,
        auto_write_tags_on_apply: settings.autoWriteTagsOnApply,
        verify_after_write: settings.verifyAfterWrite,
        protected_paths: settings.protectedPaths
          .split('\n')
          .map((p) => p.trim())
          .filter(Boolean),
      };

      const response = await api.updateConfig(updates);

      let nextSettings = settings;
      if (settings.openaiApiKey && response.openai_api_key) {
        setSavedApiKeyMask(response.openai_api_key);
        nextSettings = { ...settings, openaiApiKey: '' };
        setSettings(nextSettings);
      }

      setSavedSnapshot(JSON.stringify(nextSettings));
      setSaved(true);
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          setSaved(false);
        }
        timeoutRef.current = null;
      }, 3000);
      return true;
    } catch (error) {
      if (error instanceof api.ApiError && error.status === 401) {
        navigate('/login');
        return false;
      }
      console.error('Failed to save settings:', error);
      alert('Failed to save settings. Please try again.');
      return false;
    }
  };

  const handleReset = () => {
    if (!confirm('Reset all settings to defaults?')) return;

    setSettings(initialSettings);
    setSaved(false);
    setLibraryPathError(null);
    setOpenaiKeyError(null);
    setExtensionsError(null);
    setExcludePatternError(null);
  };

  // -------------------------------------------------------------------------
  // Extensions and exclude patterns
  // -------------------------------------------------------------------------

  const handleAddExtension = () => {
    const normalized = normalizeExtension(extensionsInput);
    if (!normalized) {
      setExtensionsError('Enter a file extension.');
      return;
    }
    if (!/^\.[a-z0-9]+$/i.test(normalized)) {
      setExtensionsError('Use letters or numbers, like .m4b');
      return;
    }
    if (settings.supportedExtensions.includes(normalized)) {
      setExtensionsError('Extension already added.');
      return;
    }
    setSettings((prev) => ({
      ...prev,
      supportedExtensions: [...prev.supportedExtensions, normalized].sort(),
    }));
    setExtensionsInput('');
    setExtensionsError(null);
    setSaved(false);
  };

  const handleRemoveExtension = (extension: string) => {
    setSettings((prev) => ({
      ...prev,
      supportedExtensions: prev.supportedExtensions.filter(
        (item) => item !== extension
      ),
    }));
    setExtensionsError(null);
    setSaved(false);
  };

  const handleAddExcludePattern = () => {
    const normalized = excludePatternInput.trim();
    if (!normalized) {
      setExcludePatternError('Enter a pattern to exclude.');
      return;
    }
    if (settings.excludePatterns.includes(normalized)) {
      setExcludePatternError('Pattern already added.');
      return;
    }
    setSettings((prev) => ({
      ...prev,
      excludePatterns: [...prev.excludePatterns, normalized],
    }));
    setExcludePatternInput('');
    setExcludePatternError(null);
    setSaved(false);
  };

  const handleRemoveExcludePattern = (pattern: string) => {
    setSettings((prev) => ({
      ...prev,
      excludePatterns: prev.excludePatterns.filter((item) => item !== pattern),
    }));
    setExcludePatternError(null);
    setSaved(false);
  };

  // -------------------------------------------------------------------------
  // AI connection test
  // -------------------------------------------------------------------------

  const handleTestAIConnection = async () => {
    const apiKey = settings.openaiApiKey.trim();
    if (!settings.enableAIParsing) {
      setOpenaiTestState({
        status: 'error',
        message: 'Enable AI parsing to test the connection.',
      });
      return;
    }
    if (apiKey && !isValidOpenAIKey(apiKey)) {
      setOpenaiKeyError('Invalid API key format.');
      return;
    }
    if (!apiKey && !savedApiKeyMask) {
      setOpenaiTestState({
        status: 'error',
        message: 'API key not provided.',
      });
      return;
    }
    setOpenaiTestState({ status: 'loading' });
    try {
      const response = await api.testAIConnection(apiKey || undefined);
      setOpenaiTestState({
        status: 'success',
        message: response.message || 'Connection successful.',
      });
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Connection failed.';
      setOpenaiTestState({
        status: 'error',
        message,
      });
    }
  };

  // -------------------------------------------------------------------------
  // Settings import / export
  // -------------------------------------------------------------------------

  const sanitizeImportPayload = (
    payload: Partial<api.Config>
  ): Partial<api.Config> => {
    const allowed = new Set([
      'root_dir', 'playlist_dir', 'organization_strategy', 'scan_on_startup', 'auto_organize',
      'folder_naming_pattern', 'file_naming_pattern', 'create_backups', 'supported_extensions',
      'exclude_patterns', 'enable_disk_quota', 'disk_quota_percent', 'enable_user_quotas',
      'default_user_quota_gb', 'auto_fetch_metadata', 'enable_ai_parsing',
      'metadata_llm_scoring_enabled', 'openai_api_key', 'metadata_sources', 'language',
      'concurrent_scans', 'memory_limit_type', 'cache_size', 'cache_invalidate_on_book_update',
      'metadata_fetch_cache_ttl_days', 'memory_limit_percent', 'memory_limit_mb',
      'purge_soft_deleted_after_days', 'purge_soft_deleted_delete_files', 'log_level', 'log_format',
      'enable_json_logging', 'auto_update_enabled', 'auto_update_channel', 'auto_update_check_minutes',
      'auto_update_window_start', 'auto_update_window_end', 'maintenance_window_enabled',
      'maintenance_window_start', 'maintenance_window_end', 'path_format', 'segment_title_format',
      'auto_rename_on_apply', 'auto_write_tags_on_apply', 'verify_after_write', 'protected_paths',
    ]);

    const cleaned: Partial<api.Config> = {};
    if (!payload || typeof payload !== 'object') return cleaned;

    for (const key of Object.keys(payload)) {
      if (!allowed.has(key)) continue;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const val = (payload as any)[key];

      switch (key) {
        case 'root_dir':
        case 'playlist_dir':
        case 'organization_strategy':
        case 'folder_naming_pattern':
        case 'file_naming_pattern':
        case 'language':
        case 'memory_limit_type':
        case 'log_level':
        case 'log_format':
        case 'auto_update_channel':
        case 'path_format':
        case 'segment_title_format':
        case 'protected_paths':
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          if (typeof val === 'string') (cleaned as any)[key] = val;
          break;

        case 'supported_extensions':
        case 'exclude_patterns':
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          if (Array.isArray(val)) (cleaned as any)[key] = val.filter((x) => typeof x === 'string');
          break;

        case 'metadata_sources':
          if (Array.isArray(val)) {
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            const sanitizedSources = (val as any[]).map((s) => {
              if (!s || typeof s !== 'object') return null;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              const src: any = {};
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              if (typeof (s as any).id === 'string') src.id = (s as any).id;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              if (typeof (s as any).name === 'string') src.name = (s as any).name;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              src.enabled = Boolean((s as any).enabled);
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              src.priority = typeof (s as any).priority === 'number' ? (s as any).priority : 0;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              src.requires_auth = Boolean((s as any).requires_auth ?? (s as any).requiresAuth);
              src.credentials = {};
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              if ((s as any).credentials && typeof (s as any).credentials === 'object') {
                // eslint-disable-next-line @typescript-eslint/no-explicit-any
                for (const [ck, cv] of Object.entries((s as any).credentials)) {
                  if (typeof cv === 'string') src.credentials[ck] = cv;
                }
              }
              return src;
            }).filter(Boolean);
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (cleaned as any)[key] = sanitizedSources;
          }
          break;

        case 'openai_api_key':
          if (typeof val === 'string') {
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            if (!val.includes('***')) (cleaned as any).openai_api_key = val;
          }
          break;

        // boolean flags
        case 'scan_on_startup':
        case 'auto_organize':
        case 'create_backups':
        case 'enable_disk_quota':
        case 'enable_user_quotas':
        case 'auto_fetch_metadata':
        case 'enable_ai_parsing':
        case 'metadata_llm_scoring_enabled':
        case 'cache_invalidate_on_book_update':
        case 'purge_soft_deleted_delete_files':
        case 'enable_json_logging':
        case 'auto_update_enabled':
        case 'maintenance_window_enabled':
        case 'auto_rename_on_apply':
        case 'auto_write_tags_on_apply':
        case 'verify_after_write':
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (cleaned as any)[key] = Boolean(val);
          break;

        // numeric fields
        case 'disk_quota_percent':
        case 'default_user_quota_gb':
        case 'concurrent_scans':
        case 'cache_size':
        case 'metadata_fetch_cache_ttl_days':
        case 'memory_limit_percent':
        case 'memory_limit_mb':
        case 'purge_soft_deleted_after_days':
        case 'auto_update_check_minutes':
        case 'auto_update_window_start':
        case 'auto_update_window_end':
        case 'maintenance_window_start':
        case 'maintenance_window_end':
          if (typeof val === 'number') {
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (cleaned as any)[key] = val;
          } else if (typeof val === 'string' && val.trim() !== '' && !isNaN(Number(val))) {
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            (cleaned as any)[key] = Number(val);
          }
          break;

        default:
          break;
      }
    }

    return cleaned;
  };

  const handleExportSettings = async () => {
    setExportInProgress(true);
    setImportNotice(null);
    try {
      const config = await api.getConfig();
      const blob = new Blob([JSON.stringify(config, null, 2)], {
        type: 'application/json',
      });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = `settings-${new Date().toISOString()}.json`;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
      setImportNotice('Settings exported.');
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Export failed.';
      setImportNotice(message);
    } finally {
      setExportInProgress(false);
    }
  };

  const handleImportClick = () => {
    setImportNotice(null);
    if (importInputRef.current) {
      importInputRef.current.value = '';
      importInputRef.current.click();
    }
  };

  const handleImportFileChange = async (
    event: ChangeEvent<HTMLInputElement>
  ) => {
    const file = event.target.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      const parsed = JSON.parse(text) as Partial<api.Config>;
      const cleaned = sanitizeImportPayload(parsed);
      setImportFileName(file.name);
      setImportPayload(cleaned);
      setImportDialogOpen(true);
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Import failed.';
      setImportNotice(message);
    }
  };

  const handleConfirmImport = async () => {
    if (!importPayload) return;
    setImportInProgress(true);
    try {
      await api.updateConfig(importPayload);
      await loadConfig();
      setImportDialogOpen(false);
      setImportPayload(null);
      setImportFileName('');
      setImportNotice('Settings imported successfully.');
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Import failed.';
      setImportNotice(message);
    } finally {
      setImportInProgress(false);
    }
  };

  // -------------------------------------------------------------------------
  // Navigation blocker handlers
  // -------------------------------------------------------------------------

  const handleSaveAndNavigate = async () => {
    const success = await handleSave();
    if (success) {
      blocker.proceed?.();
    }
  };

  const handleDiscardNavigation = () => {
    blocker.proceed?.();
  };

  const handleCancelNavigation = () => {
    blocker.reset?.();
  };

  // -------------------------------------------------------------------------
  // Return
  // -------------------------------------------------------------------------

  return {
    normalizeExtension,
    isValidOpenAIKey,
    handleChange,
    handleDedupChange,
    handleEmbeddingChange,
    handleMetadataScoringChange,
    handleMaintenanceChange,
    handleScheduledChange,
    handleToolsChange,
    handleBrowseLibraryPath,
    handleBrowserSelect,
    handleBrowserConfirm,
    handleBrowserCancel,
    loadImportFolders,
    handleAddImportFolder,
    handleRemoveImportFolder,
    handleScanImportFolder,
    handleRequestCancelScan,
    handleConfirmCancelScan,
    handleViewScanErrors,
    loadBackups,
    handleCreateBackup,
    handleRequestRestore,
    handleConfirmRestore,
    handleRequestDeleteBackup,
    handleConfirmDeleteBackup,
    handleFolderBrowserSelect,
    handleSourceToggle,
    handleTestMetadataSource,
    handleCredentialChange,
    handleSourceReorder,
    handleSave,
    handleReset,
    handleAddExtension,
    handleRemoveExtension,
    handleAddExcludePattern,
    handleRemoveExcludePattern,
    handleTestAIConnection,
    handleExportSettings,
    handleImportClick,
    handleImportFileChange,
    handleConfirmImport,
    handleSaveAndNavigate,
    handleDiscardNavigation,
    handleCancelNavigation,
  };
}
