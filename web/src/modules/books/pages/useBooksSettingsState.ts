// file: web/src/modules/books/pages/useBooksSettingsState.ts
// version: 0.1.0
// guid: c74aed84-d8cc-4d1f-a927-f141f3bc829f
// last-edited: 2026-11-21

// All of the Settings page's state, loading, saving and handlers, moved verbatim out of
// pages/Settings.tsx so the shell frame holds none of it. Offered to the panels, the
// save bar and the dialogs through BooksSettingsProvider.

import { useState, useEffect, useMemo, useRef } from 'react';
import { useNavigate } from 'react-router';
import { useUnsavedChangesBlocker } from '../../../hooks/useUnsavedChangesBlocker';
import {
  useSettingsHandlers,
  ScanStatus,
  ScanErrorTarget,
} from '../../../hooks/useSettingsHandlers';
import * as api from '../../../services/api';
import type { SecretStatus } from '../../../services/configView';
import { defaultMetadataSources, uiSourcesFromConfig } from '../../../utils/metadataSources';
import type { SettingsState } from './settingsState';

export function useBooksSettingsState() {
  const navigate = useNavigate();
  const [browserOpen, setBrowserOpen] = useState(false);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const scanIntervalsRef = useRef<Record<number, number>>({});

  // Import folder management
  const [importPaths, setImportFolders] = useState<api.ImportPath[]>([]);
  const [addFolderDialogOpen, setAddFolderDialogOpen] = useState(false);
  const [newFolderPath, setNewFolderPath] = useState('');
  const [showFolderBrowser, setShowFolderBrowser] = useState(false);
  const [scanStatuses, setScanStatuses] = useState<Record<number, ScanStatus>>({});
  const [cancelScanTarget, setCancelScanTarget] = useState<api.ImportPath | null>(null);
  const [scanErrorTarget, setScanErrorTarget] = useState<ScanErrorTarget | null>(null);
  const [backups, setBackups] = useState<api.BackupInfo[]>([]);
  const [backupsLoading, setBackupsLoading] = useState(false);
  const [backupNotice, setBackupNotice] = useState<{
    severity: 'success' | 'error' | 'info';
    message: string;
  } | null>(null);
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
  const [restoreTarget, setRestoreTarget] = useState<api.BackupInfo | null>(null);
  const [restoreInProgress, setRestoreInProgress] = useState(false);
  const [restoreVerify, setRestoreVerify] = useState(true);
  const [deleteBackupTarget, setDeleteBackupTarget] = useState<api.BackupInfo | null>(null);
  const [deleteBackupInProgress, setDeleteBackupInProgress] = useState(false);
  const [createBackupInProgress, setCreateBackupInProgress] = useState(false);
  const [openaiTestState, setOpenaiTestState] = useState<{
    status: 'idle' | 'loading' | 'success' | 'error';
    message?: string;
    model?: string;
  }>({ status: 'idle' });
  const [libraryPathError, setLibraryPathError] = useState<string | null>(null);
  const [openaiKeyError, setOpenaiKeyError] = useState<string | null>(null);
  const [extensionsInput, setExtensionsInput] = useState('');
  const [excludePatternInput, setExcludePatternInput] = useState('');
  const [extensionsError, setExtensionsError] = useState<string | null>(null);
  const [excludePatternError, setExcludePatternError] = useState<string | null>(null);
  const [importDialogOpen, setImportDialogOpen] = useState(false);
  const [importPayload, setImportPayload] = useState<Partial<api.Config> | null>(null);
  const [importFileName, setImportFileName] = useState('');
  const [importNotice, setImportNotice] = useState<string | null>(null);
  const [exportInProgress, setExportInProgress] = useState(false);
  const [importInProgress, setImportInProgress] = useState(false);
  const [savedSnapshot, setSavedSnapshot] = useState('');
  const [configLoaded, setConfigLoaded] = useState(false);
  // What the secret store says about the AcoustID.org key: set or not, never the key.
  const [acoustidKeyStatus, setAcoustidKeyStatus] = useState<SecretStatus | undefined>();
  // Config keys the server's environment is currently forcing; their controls
  // render disabled. Server-reported and read-only — never saved back.
  const [envLocked, setEnvLocked] = useState<string[]>([]);
  // Same keys as envLocked, mapped to WHAT locks each one, so a disabled control
  // can name the environment variable or the flag instead of just going grey.
  const [settingLocks, setSettingLocks] = useState<Record<string, string>>({});
  // Server-reported (the path builder's own list); undefined until GET /config answers.
  const [namingPatternTokens, setNamingPatternTokens] = useState<string[] | undefined>();
  const importInputRef = useRef<HTMLInputElement | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  const initialSettings: SettingsState = {
    // Library settings
    libraryPath: '/path/to/audiobooks/library',
    // Empty until the server reports the real one. Seeding the binary's own
    // default ("audiobooks.pebble") here would show every operator a relative
    // path that is almost certainly not where their database is.
    databasePath: '',
    // 'auto', 'copy', 'hardlink', 'reflink', 'symlink'
    organizationStrategy: 'auto',
    scanOnStartup: false,
    autoOrganize: true,
    folderNamingPattern: '{author}/{series}/{title} ({print_year})',
    fileNamingPattern: '{title} - {author} - read by {narrator}',
    createBackups: true,
    supportedExtensions: ['.m4b', '.mp3', '.m4a'],
    excludePatterns: [],

    // Storage quotas
    enableDiskQuota: false,
    diskQuotaPercent: 80,
    enableUserQuotas: false,

    // Metadata settings
    enableAIParsing: false,
    openaiApiKey: '',
    metadataSources: defaultMetadataSources(),

    // Performance settings
    concurrentScans: 4,

    // Caching
    cacheInvalidateOnBookUpdate: false,
    metadataFetchCacheTTLDays: 30,

    // Lifecycle / retention
    purgeSoftDeletedAfterDays: 30,
    backupRetentionDays: 0,
    purgeSoftDeletedDeleteFiles: false,

    // Logging
    logLevel: 'info',

    // Auto-update
    autoUpdateEnabled: false,
    autoUpdateChannel: 'stable',
    autoUpdateCheckMinutes: 60,

    // Maintenance window
    maintenanceWindowEnabled: false,
    maintenanceWindowStart: 2,
    maintenanceWindowEnd: 4,

    // Smart apply pipeline
    autoRenameOnApply: true,
    autoWriteTagsOnApply: true,
    verifyAfterWrite: true,

    // Deluge integration
    protectedPaths: '',
  };

  const [settings, setSettings] = useState<SettingsState>(initialSettings);
  const [saved, setSaved] = useState(false);
  const [expandedSource, setExpandedSource] = useState<string | null>(null);
  const [sourceTestStatus, setSourceTestStatus] = useState<
    Record<
      string,
      { testing: boolean; result?: { success: boolean; message?: string; error?: string } }
    >
  >({});
  // What the secret store says about the OpenAI key: set or not, never the key.
  const [openaiKeyStatus, setOpenaiKeyStatus] = useState<SecretStatus | undefined>();

  // Nested config state (CFG-2)
  const [dedupConfig, setDedupConfig] = useState<api.DedupConfig>({
    book_high_threshold: 0.92,
    book_low_threshold: 0.7,
    author_high_threshold: 0.92,
    author_low_threshold: 0.7,
    auto_merge_enabled: false,
    embeddings_enabled: false,
    llm_auto_merge_high_confidence: false,
    on_import_via_scheduler: false,
    review_model: 'gpt-5-mini',
    signals: {
      // Composite-score bands are on the server's 0–100 scale
      // (internal/dedup/unified.DefaultScoreConfig: 97/90/75/60), not 0–1.
      // These are only the pre-load placeholders; the fetched config replaces them.
      band_certain_min: 97,
      band_high_min: 90,
      band_medium_min: 75,
      band_review_min: 60,
    },
  });
  const [embeddingConfig, setEmbeddingConfig] = useState<api.EmbeddingConfig>({
    enabled: false,
    model: 'text-embedding-3-large',
    dimensions: 3072,
    base_url: '',
    vector_backend: 'hnsw',
    request_timeout_seconds: 0,
  });
  const [aiBackendConfig, setAIBackendConfig] = useState<api.AIBackendConfig>({
    embedding_mode: 'disabled',
    llm_mode: 'disabled',
    local_base_url: '',
    local_embedding_model: '',
    local_llm_model: '',
  });
  const [metadataScoringConfig, setMetadataScoringConfig] = useState<api.MetadataScoringConfig>({
    embedding_enabled: false,
    embedding_min_score: 0.82,
    embedding_best_match: 0.88,
    llm_enabled: false,
    llm_rerank_epsilon: 0.05,
    llm_rerank_top_k: 5,
  });
  const [maintenanceConfig, setMaintenanceConfig] = useState<api.MaintenanceConfig>({
    enabled: true,
    window_start: 2,
    window_end: 5,
    dedup_refresh: true,
    series_prune: true,
    author_split: true,
    tombstone_cleanup: true,
    reconcile: false,
    purge_deleted: false,
    purge_old_logs: true,
    db_optimize: true,
    library_scan: false,
    library_organize: false,
    metadata_refresh: false,
    library_size_refresh: true,
    acoustid_online_lookup: false,
    acoustid_nightly_limit: 200,
  });
  const [scheduledConfig, setScheduledConfig] = useState<api.ScheduledTasksConfig | null>(null);
  const [toolsConfig, setToolsConfig] = useState<api.ToolsConfig>({
    managed_dir: '/var/lib/audiobook-organizer/tools',
  });

  const settingsSnapshot = useMemo(() => JSON.stringify(settings), [settings]);
  const hasUnsavedChanges = savedSnapshot !== '' && settingsSnapshot !== savedSnapshot;
  const blocker = useUnsavedChangesBlocker(hasUnsavedChanges);
  const savedSettings = useMemo(() => {
    if (!savedSnapshot) return null;
    try {
      return JSON.parse(savedSnapshot) as SettingsState;
    } catch {
      return null;
    }
  }, [savedSnapshot]);
  const importKeys = useMemo(() => {
    if (!importPayload) return [];
    return Object.keys(importPayload);
  }, [importPayload]);

  // Load configuration on mount
  useEffect(() => {
    loadConfig();
    loadImportFolders();
    loadBackups();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!hasUnsavedChanges) return;
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handleBeforeUnload);
    return () => window.removeEventListener('beforeunload', handleBeforeUnload);
  }, [hasUnsavedChanges]);

  // Cleanup save timeout + scan intervals on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
      // Clear all active scan intervals
      Object.values(scanIntervalsRef.current).forEach((interval) => {
        window.clearInterval(interval);
      });
      scanIntervalsRef.current = {};
    };
  }, []);

  const loadConfig = async () => {
    try {
      const config = await api.getConfig();
      // Which settings the server's environment is forcing. Kept out of
      // SettingsState because it is server-reported and read-only: it must never
      // be part of the dirty-check or the save payload.
      setEnvLocked(config.env_locked ?? []);
      setSettingLocks(config.setting_locks ?? {});
      setNamingPatternTokens(config.naming_pattern_tokens);
      // Secrets are write-only: the server reports whether each is set, never its value.
      setOpenaiKeyStatus(config.secret_status?.openai_api_key);
      // Map all backend config fields to frontend settings format
      const nextSettings: SettingsState = {
        // Library settings
        libraryPath: config.root_dir || '',
        databasePath: config.database_path || '',
        organizationStrategy: config.organization_strategy || 'auto',
        scanOnStartup: config.scan_on_startup ?? false,
        autoOrganize: config.auto_organize ?? true,
        folderNamingPattern:
          config.folder_naming_pattern || '{author}/{series}/{title} ({print_year})',
        fileNamingPattern: config.file_naming_pattern || '{title} - {author} - read by {narrator}',
        createBackups: config.create_backups ?? true,
        supportedExtensions: config.supported_extensions?.length
          ? config.supported_extensions
          : ['.m4b', '.mp3', '.m4a'],
        excludePatterns: config.exclude_patterns || [],

        // Storage quotas
        enableDiskQuota: config.enable_disk_quota ?? false,
        diskQuotaPercent: config.disk_quota_percent || 80,
        enableUserQuotas: config.enable_user_quotas ?? false,

        // Metadata settings
        enableAIParsing: config.enable_ai_parsing ?? false,
        openaiApiKey: '', // Clear field when loading, show placeholder instead
        metadataSources: uiSourcesFromConfig(config.metadata_sources),

        // Performance settings
        concurrentScans: config.concurrent_scans || 4,

        // Memory management
        cacheInvalidateOnBookUpdate: config.cache_invalidate_on_book_update ?? false,
        metadataFetchCacheTTLDays: config.metadata_fetch_cache_ttl_days ?? 30,

        // Lifecycle / retention
        purgeSoftDeletedAfterDays: config.purge_soft_deleted_after_days ?? 30,
        backupRetentionDays: config.backup_retention_days ?? 0,
        purgeSoftDeletedDeleteFiles: config.purge_soft_deleted_delete_files ?? false,

        // Logging
        logLevel: config.log_level || 'info',

        // Auto-update. The server has only ever emitted the nested object; the
        // flat auto_update_* keys this used to fall back to were never sent.
        autoUpdateEnabled: config.auto_update?.enabled ?? false,
        autoUpdateChannel: config.auto_update?.channel ?? 'stable',
        autoUpdateCheckMinutes: config.auto_update?.check_minutes ?? 60,

        // Maintenance window, nested object only (same reason).
        maintenanceWindowEnabled: config.maintenance?.enabled ?? false,
        maintenanceWindowStart: config.maintenance?.window_start ?? 2,
        maintenanceWindowEnd: config.maintenance?.window_end ?? 4,

        // Smart apply pipeline
        autoRenameOnApply: config.auto_rename_on_apply ?? true,
        autoWriteTagsOnApply: config.auto_write_tags_on_apply ?? true,
        verifyAfterWrite: config.verify_after_write ?? true,

        // Deluge integration
        protectedPaths: (config.protected_paths || []).join('\n'),
      };
      setAcoustidKeyStatus(config.secret_status?.acoustid_api_key);
      if (config.dedup) setDedupConfig(config.dedup);
      if (config.embedding) setEmbeddingConfig(config.embedding);
      if (config.ai_backend) setAIBackendConfig(config.ai_backend);
      if (config.metadata_scoring) setMetadataScoringConfig(config.metadata_scoring);
      if (config.maintenance) setMaintenanceConfig(config.maintenance);
      if (config.scheduled) setScheduledConfig(config.scheduled);
      if (config.tools) setToolsConfig(config.tools);
      setSettings(nextSettings);
      setSavedSnapshot(JSON.stringify(nextSettings));
      setConfigLoaded(true);
    } catch (error) {
      if (error instanceof api.ApiError && error.status === 401) {
        navigate('/login');
        return;
      }
      console.error('Failed to load config:', error);
    }
  };

  const {
    handleChange,
    handleDedupChange,
    handleEmbeddingChange,
    handleAIBackendChange,
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
    handleToggleClearCredential,
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
    handleClearOpenAIKey,
  } = useSettingsHandlers({
    settings,
    setSettings,
    setSaved,
    setOpenaiKeyStatus,
    setConfigLoaded,
    setDedupConfig,
    setEmbeddingConfig,
    setAIBackendConfig,
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
    setExpandedSource,
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
    openaiKeyStatus,
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
    importFileName,
    savedSettings,
    extensionsInput,
    excludePatternInput,
    newFolderPath,
    selectedPath,
    blocker,
    dedupConfig,
    embeddingConfig,
    aiBackendConfig,
    metadataScoringConfig,
    maintenanceConfig,
    scheduledConfig,
    toolsConfig,
    importInputRef,
    loadConfig,
    initialSettings,
  });

  return {
    acoustidKeyStatus,
    addFolderDialogOpen,
    aiBackendConfig,
    backupNotice,
    backups,
    backupsLoading,
    blocker,
    browserOpen,
    cancelScanTarget,
    configLoaded,
    createBackupInProgress,
    dedupConfig,
    deleteBackupInProgress,
    deleteBackupTarget,
    embeddingConfig,
    envLocked,
    excludePatternError,
    excludePatternInput,
    expandedSource,
    exportInProgress,
    extensionsError,
    extensionsInput,
    handleAIBackendChange,
    handleAddExcludePattern,
    handleAddExtension,
    handleAddImportFolder,
    handleBrowseLibraryPath,
    handleBrowserCancel,
    handleBrowserConfirm,
    handleBrowserSelect,
    handleCancelNavigation,
    handleChange,
    handleConfirmCancelScan,
    handleConfirmDeleteBackup,
    handleConfirmImport,
    handleConfirmRestore,
    handleCreateBackup,
    handleClearOpenAIKey,
    handleCredentialChange,
    handleDedupChange,
    handleDiscardNavigation,
    handleEmbeddingChange,
    handleExportSettings,
    handleFolderBrowserSelect,
    handleImportClick,
    handleImportFileChange,
    handleMaintenanceChange,
    handleMetadataScoringChange,
    handleRemoveExcludePattern,
    handleRemoveExtension,
    handleRemoveImportFolder,
    handleRequestCancelScan,
    handleRequestDeleteBackup,
    handleRequestRestore,
    handleReset,
    handleSave,
    handleSaveAndNavigate,
    handleScanImportFolder,
    handleScheduledChange,
    handleSourceReorder,
    handleSourceToggle,
    handleTestAIConnection,
    handleTestMetadataSource,
    handleToggleClearCredential,
    handleToolsChange,
    handleViewScanErrors,
    hasUnsavedChanges,
    importDialogOpen,
    importFileName,
    importInProgress,
    importInputRef,
    importKeys,
    importNotice,
    importPaths,
    libraryPathError,
    maintenanceConfig,
    metadataScoringConfig,
    namingPatternTokens,
    newFolderPath,
    openaiKeyError,
    openaiKeyStatus,
    openaiTestState,
    restoreDialogOpen,
    restoreInProgress,
    restoreTarget,
    restoreVerify,
    saved,
    savedSnapshot,
    scanErrorTarget,
    scanStatuses,
    scheduledConfig,
    selectedPath,
    setAddFolderDialogOpen,
    setCancelScanTarget,
    setDeleteBackupTarget,
    setExcludePatternInput,
    setExpandedSource,
    setExtensionsInput,
    setImportDialogOpen,
    setNewFolderPath,
    setRestoreDialogOpen,
    setRestoreVerify,
    setScanErrorTarget,
    setSettings,
    setShowFolderBrowser,
    settingLocks,
    settings,
    showFolderBrowser,
    sourceTestStatus,
    toolsConfig,
  };
}
