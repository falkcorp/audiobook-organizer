// file: web/src/pages/Settings.tsx
// version: 1.53.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-06-19

import { useState, useEffect, useMemo, useRef, ChangeEvent } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { useUnsavedChangesBlocker } from '../hooks/useUnsavedChangesBlocker';
import { useSettingsHandlers } from '../hooks/useSettingsHandlers';
import type { ScanStatus, ScanErrorTarget } from '../hooks/useSettingsHandlers';
import {
  Box,
  Typography,
  Paper,
  Tabs,
  Tab,
  TextField,
  Button,
  Switch,
  FormControlLabel,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  List,
  ListItem,
  ListItemText,
} from '@mui/material';
import * as api from '../services/api';
import { ServerFileBrowser } from '../components/common/ServerFileBrowser';
import { SettingsGeneral } from '../components/SettingsGeneral';
import BlockedHashesTab from '../components/settings/BlockedHashesTab';
import { TempLoginTab } from '../components/settings/TempLoginTab';
import PluginsTab from '../components/settings/PluginsTab';
import { PathsSettingsTab } from '../components/settings/PathsSettingsTab';
import { MetadataSettingsTab } from '../components/settings/MetadataSettingsTab';
import { ToolsSettingsTab } from '../components/settings/ToolsSettingsTab';
import { ITunesImport } from '../components/settings/ITunesImport';
import { ITunesTransfer } from '../components/settings/ITunesTransfer';
import { SystemInfoTab } from '../components/system/SystemInfoTab';
import { EmbeddingSettingsSection } from '../components/settings/EmbeddingSettingsSection';
import { DedupSettingsSection } from '../components/settings/DedupSettingsSection';
import { MetadataScoringSection } from '../components/settings/MetadataScoringSection';
import { MaintenanceSettingsSection } from '../components/settings/MaintenanceSettingsSection';
import { AutoUpdateSection } from '../components/settings/AutoUpdateSection';
import { ScheduledTasksSection } from '../components/settings/ScheduledTasksSection';
import { APIKeysTab } from '../components/settings/APIKeysTab';
import { PerformanceSettingsTab } from '../components/settings/PerformanceSettingsTab';
import {
  Save as SaveIcon,
  RestartAlt as RestartAltIcon,
  FolderOpen as FolderOpenIcon,
  ExpandMore as ExpandMoreIcon,
} from '@mui/icons-material';
import {
  Accordion,
  AccordionSummary,
  AccordionDetails,
} from '@mui/material';

interface TabPanelProps {
  children?: React.ReactNode;
  index: number;
  value: number;
}


function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;

  return (
    <Box
      role="tabpanel"
      hidden={value !== index}
      id={`settings-tabpanel-${index}`}
      aria-labelledby={`settings-tab-${index}`}
      sx={{
        overflowY: 'auto',
        overflowX: 'hidden',
        flex: 1,
        minHeight: 0,
        p: 3,
      }}
      {...other}
    >
      {value === index && children}
    </Box>
  );
}


const TAB_KEYS = ['library', 'itunes', 'metadata', 'dedup', 'paths', 'performance', 'security', 'api-keys', 'plugins', 'tools', 'system'] as const;

function tabFromHash(hash: string): number {
  const key = hash.replace('#', '');
  const idx = TAB_KEYS.indexOf(key as (typeof TAB_KEYS)[number]);
  return idx >= 0 ? idx : 0;
}

interface UiMetadataSource {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  requiresAuth: boolean;
  credentials: { [key: string]: string };
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
  metadataSources: UiMetadataSource[];
  language: string;
  concurrentScans: number;
  memoryLimitType: string;
  cacheSize: number;
  cacheInvalidateOnBookUpdate: boolean;
  metadataFetchCacheTTLDays: number;
  memoryLimitPercent: number;
  memoryLimitMB: number;
  logLevel: string;
  logFormat: string;
  enableJsonLogging: boolean;
  purgeSoftDeletedAfterDays: number;
  purgeSoftDeletedDeleteFiles: boolean;
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

  // Deluge integration
  protectedPaths: string;
}

export function Settings() {
  const navigate = useNavigate();
  const location = useLocation();
  const [tabValue, setTabValue] = useState(() => tabFromHash(location.hash));
  const [browserOpen, setBrowserOpen] = useState(false);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const scanIntervalsRef = useRef<Record<number, number>>({});

  // Import folder management
  const [importPaths, setImportFolders] = useState<api.ImportPath[]>([]);
  const [addFolderDialogOpen, setAddFolderDialogOpen] = useState(false);
  const [newFolderPath, setNewFolderPath] = useState('');
  const [showFolderBrowser, setShowFolderBrowser] = useState(false);
  const [scanStatuses, setScanStatuses] = useState<
    Record<number, ScanStatus>
  >({});
  const [cancelScanTarget, setCancelScanTarget] =
    useState<api.ImportPath | null>(null);
  const [scanErrorTarget, setScanErrorTarget] =
    useState<ScanErrorTarget | null>(null);
  const [backups, setBackups] = useState<api.BackupInfo[]>([]);
  const [backupsLoading, setBackupsLoading] = useState(false);
  const [backupNotice, setBackupNotice] = useState<{
    severity: 'success' | 'error' | 'info';
    message: string;
  } | null>(null);
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
  const [restoreTarget, setRestoreTarget] = useState<api.BackupInfo | null>(
    null
  );
  const [restoreInProgress, setRestoreInProgress] = useState(false);
  const [restoreVerify, setRestoreVerify] = useState(true);
  const [deleteBackupTarget, setDeleteBackupTarget] =
    useState<api.BackupInfo | null>(null);
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
  const [excludePatternError, setExcludePatternError] =
    useState<string | null>(null);
  const [importDialogOpen, setImportDialogOpen] = useState(false);
  const [importPayload, setImportPayload] =
    useState<Partial<api.Config> | null>(null);
  const [importFileName, setImportFileName] = useState('');
  const [importNotice, setImportNotice] = useState<string | null>(null);
  const [exportInProgress, setExportInProgress] = useState(false);
  const [importInProgress, setImportInProgress] = useState(false);
  const [savedSnapshot, setSavedSnapshot] = useState('');
  const [configLoaded, setConfigLoaded] = useState(false);
  const importInputRef = useRef<HTMLInputElement | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  // Factory reset state
  const [factoryResetStep, setFactoryResetStep] = useState<0 | 1 | 2>(0);
  const [factoryResetConfirmText, setFactoryResetConfirmText] = useState('');
  const [factoryResetInProgress, setFactoryResetInProgress] = useState(false);

  const initialSettings: SettingsState = {
    // Library settings
    libraryPath: '/path/to/audiobooks/library',
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
    defaultUserQuotaGB: 100,

    // Metadata settings
    autoFetchMetadata: true,
    enableAIParsing: false,
    metadataLLMScoringEnabled: false,
    openaiApiKey: '',
    metadataSources: [
      {
        id: 'audible',
        name: 'Audible',
        enabled: true,
        priority: 1,
        requiresAuth: false,
        credentials: {},
      },
      {
        id: 'openlibrary',
        name: 'Open Library',
        enabled: true,
        priority: 2,
        requiresAuth: false,
        credentials: {},
      },
      {
        id: 'audnexus',
        name: 'Audnexus',
        enabled: true,
        priority: 3,
        requiresAuth: false,
        credentials: {},
      },
      {
        id: 'google-books',
        name: 'Google Books',
        enabled: false,
        priority: 4,
        requiresAuth: true,
        credentials: { apiKey: '' },
      },
      {
        id: 'hardcover',
        name: 'Hardcover',
        enabled: false,
        priority: 5,
        requiresAuth: true,
        credentials: { apiKey: '' },
      },
    ],
    language: 'en',

    // Performance settings
    concurrentScans: 4,

    // Memory management
    // 'items', 'percent', 'absolute'
    memoryLimitType: 'items',
    cacheSize: 1000, // items
    cacheInvalidateOnBookUpdate: false,
    metadataFetchCacheTTLDays: 30,
    memoryLimitPercent: 25, // % of system memory
    memoryLimitMB: 512, // MB

    // Lifecycle / retention
    purgeSoftDeletedAfterDays: 30,
    purgeSoftDeletedDeleteFiles: false,

    // Logging
    logLevel: 'info',
    logFormat: 'text', // 'text' or 'json'
    enableJsonLogging: false,

    // Auto-update
    autoUpdateEnabled: false,
    autoUpdateChannel: 'stable',
    autoUpdateCheckMinutes: 60,
    autoUpdateWindowStart: 1,
    autoUpdateWindowEnd: 4,

    // Maintenance window
    maintenanceWindowEnabled: false,
    maintenanceWindowStart: 2,
    maintenanceWindowEnd: 4,

    // Smart apply pipeline
    pathFormat: '{author}/{series_prefix}{title}/{track_title}.{ext}',
    segmentTitleFormat: '{title} - {track}/{total_tracks}',
    autoRenameOnApply: true,
    autoWriteTagsOnApply: true,
    verifyAfterWrite: true,

    // Deluge integration
    protectedPaths: '',
  };

  const [settings, setSettings] = useState<SettingsState>(initialSettings);
  const [saved, setSaved] = useState(false);
  const [expandedSource, setExpandedSource] = useState<string | null>(null);
  const [sourceTestStatus, setSourceTestStatus] = useState<Record<string, { testing: boolean; result?: { success: boolean; message?: string; error?: string } }>>({});
  const [savedApiKeyMask, setSavedApiKeyMask] = useState<string>('');

  // Nested config state (CFG-2)
  const [dedupConfig, setDedupConfig] = useState<api.DedupConfig>({
    book_high_threshold: 0.92,
    book_low_threshold: 0.70,
    author_high_threshold: 0.92,
    author_low_threshold: 0.70,
    auto_merge_enabled: false,
    embeddings_enabled: false,
    llm_auto_merge_high_confidence: false,
    on_import_via_scheduler: false,
    review_model: 'gpt-5-mini',
    signals: {
      band_certain_min: 0.97,
      band_high_min: 0.92,
      band_medium_min: 0.82,
      band_review_min: 0.70,
      duration_boost: 0.05,
      folder_path_boost: 0.03,
    },
  });
  const [embeddingConfig, setEmbeddingConfig] = useState<api.EmbeddingConfig>({
    enabled: false,
    model: 'text-embedding-3-large',
    dimensions: 3072,
    base_url: '',
    vector_backend: 'hnsw',
  });
  const [metadataScoringConfig, setMetadataScoringConfig] = useState<api.MetadataScoringConfig>({
    embedding_enabled: false,
    embedding_min_score: 0.82,
    embedding_best_match: 0.88,
    llm_enabled: false,
    llm_rerank_epsilon: 0.05,
    llm_rerank_top_k: 5,
    write_backup_before: true,
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
    embed_queue_debounce_ms: 500,
  });

  const settingsSnapshot = useMemo(
    () => JSON.stringify(settings),
    [settings]
  );
  const hasUnsavedChanges =
    savedSnapshot !== '' && settingsSnapshot !== savedSnapshot;
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
      // Store masked key if present
      if (config.openai_api_key && config.openai_api_key.includes('***')) {
        setSavedApiKeyMask(config.openai_api_key);
      }
      // Map all backend config fields to frontend settings format
      const nextSettings: SettingsState = {
        // Library settings
        libraryPath: config.root_dir || '',
        organizationStrategy: config.organization_strategy || 'auto',
        scanOnStartup: config.scan_on_startup ?? false,
        autoOrganize: config.auto_organize ?? true,
        folderNamingPattern:
          config.folder_naming_pattern ||
          '{author}/{series}/{title} ({print_year})',
        fileNamingPattern:
          config.file_naming_pattern ||
          '{title} - {author} - read by {narrator}',
        createBackups: config.create_backups ?? true,
        supportedExtensions: config.supported_extensions?.length
          ? config.supported_extensions
          : ['.m4b', '.mp3', '.m4a'],
        excludePatterns: config.exclude_patterns || [],

        // Storage quotas
        enableDiskQuota: config.enable_disk_quota ?? false,
        diskQuotaPercent: config.disk_quota_percent || 80,
        enableUserQuotas: config.enable_user_quotas ?? false,
        defaultUserQuotaGB: config.default_user_quota_gb || 100,

        // Metadata settings
        autoFetchMetadata: config.auto_fetch_metadata ?? true,
        enableAIParsing: config.enable_ai_parsing ?? false,
        metadataLLMScoringEnabled: config.metadata_llm_scoring_enabled ?? false,
        openaiApiKey: '', // Clear field when loading, show placeholder instead
        metadataSources:
          config.metadata_sources && config.metadata_sources.length > 0
            ? config.metadata_sources.map((source) => {
                // Force requiresAuth for sources that need API keys,
                // even if the saved config has requires_auth: false
                const authSources = ['google-books', 'hardcover'];
                return {
                  id: source.id,
                  name: source.name,
                  enabled: source.enabled,
                  priority: source.priority,
                  requiresAuth: source.requires_auth || authSources.includes(source.id),
                  credentials: authSources.includes(source.id)
                    ? { apiKey: '', ...source.credentials }
                    : source.credentials || ({} as { [key: string]: string }),
                };
              })
            : [
                {
                  id: 'audible',
                  name: 'Audible',
                  enabled: true,
                  priority: 1,
                  requiresAuth: false,
                  credentials: {},
                },
                {
                  id: 'openlibrary',
                  name: 'Open Library',
                  enabled: true,
                  priority: 2,
                  requiresAuth: false,
                  credentials: {},
                },
                {
                  id: 'audnexus',
                  name: 'Audnexus',
                  enabled: true,
                  priority: 3,
                  requiresAuth: false,
                  credentials: {},
                },
                {
                  id: 'google-books',
                  name: 'Google Books',
                  enabled: false,
                  priority: 4,
                  requiresAuth: true,
                  credentials: { apiKey: '' },
                },
                {
                  id: 'hardcover',
                  name: 'Hardcover',
                  enabled: false,
                  priority: 5,
                  requiresAuth: true,
                  credentials: { apiKey: '' },
                },
              ],
        language: config.language || 'en',

        // Performance settings
        concurrentScans: config.concurrent_scans || 4,

        // Memory management
        memoryLimitType: config.memory_limit_type || 'items',
        cacheSize: config.cache_size || 1000,
        cacheInvalidateOnBookUpdate: config.cache_invalidate_on_book_update ?? false,
        metadataFetchCacheTTLDays: config.metadata_fetch_cache_ttl_days ?? 30,
        memoryLimitPercent: config.memory_limit_percent || 25,
        memoryLimitMB: config.memory_limit_mb || 512,

        // Lifecycle / retention
        purgeSoftDeletedAfterDays: config.purge_soft_deleted_after_days ?? 30,
        purgeSoftDeletedDeleteFiles:
          config.purge_soft_deleted_delete_files ?? false,

        // Logging
        logLevel: config.log_level || 'info',
        logFormat: config.log_format || 'text',
        enableJsonLogging: config.enable_json_logging ?? false,

        // Auto-update (nested key preferred, flat fallback for compat)
        autoUpdateEnabled: config.auto_update?.enabled ?? config.auto_update_enabled ?? false,
        autoUpdateChannel: config.auto_update?.channel ?? config.auto_update_channel ?? 'stable',
        autoUpdateCheckMinutes: config.auto_update?.check_minutes ?? config.auto_update_check_minutes ?? 60,
        autoUpdateWindowStart: config.auto_update?.window_start ?? config.auto_update_window_start ?? 1,
        autoUpdateWindowEnd: config.auto_update?.window_end ?? config.auto_update_window_end ?? 4,

        // Maintenance window (nested key preferred, flat fallback for compat)
        maintenanceWindowEnabled: config.maintenance?.enabled ?? config.maintenance_window_enabled ?? false,
        maintenanceWindowStart: config.maintenance?.window_start ?? config.maintenance_window_start ?? 2,
        maintenanceWindowEnd: config.maintenance?.window_end ?? config.maintenance_window_end ?? 4,

        // Smart apply pipeline
        pathFormat: config.path_format || '{author}/{series_prefix}{title}/{track_title}.{ext}',
        segmentTitleFormat: config.segment_title_format || '{title} - {track}/{total_tracks}',
        autoRenameOnApply: config.auto_rename_on_apply ?? true,
        autoWriteTagsOnApply: config.auto_write_tags_on_apply ?? true,
        verifyAfterWrite: config.verify_after_write ?? true,

        // Deluge integration
        protectedPaths: (config.protected_paths || []).join('\n'),
      };
      if (config.dedup) setDedupConfig(config.dedup);
      if (config.embedding) setEmbeddingConfig(config.embedding);
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
  } = useSettingsHandlers({
    settings, setSettings, setSaved, setSavedApiKeyMask, setConfigLoaded,
    setDedupConfig, setEmbeddingConfig, setMetadataScoringConfig,
    setMaintenanceConfig, setScheduledConfig, setToolsConfig,
    setLibraryPathError, setOpenaiKeyError, setExtensionsError, setExcludePatternError,
    setImportFolders, setScanStatuses, setCancelScanTarget,
    setScanErrorTarget, setBackups, setBackupNotice, setBackupsLoading,
    setRestoreTarget, setRestoreDialogOpen, setRestoreInProgress,
    setDeleteBackupTarget, setDeleteBackupInProgress, setCreateBackupInProgress,
    setOpenaiTestState, setSavedSnapshot, setSourceTestStatus, setExpandedSource,
    setBrowserOpen, setSelectedPath, setAddFolderDialogOpen, setNewFolderPath, setShowFolderBrowser,
    setImportDialogOpen, setImportPayload, setImportFileName, setImportNotice,
    setExportInProgress, setImportInProgress,
    setExtensionsInput, setExcludePatternInput,
    savedApiKeyMask, configLoaded, navigate,
    scanIntervalsRef, isUnmountedRef, timeoutRef,
    restoreTarget, restoreVerify, deleteBackupTarget, cancelScanTarget,
    scanStatuses, importPayload, importFileName, savedSettings,
    extensionsInput, excludePatternInput, newFolderPath, selectedPath,
    blocker, dedupConfig, embeddingConfig, metadataScoringConfig,
    maintenanceConfig, scheduledConfig, toolsConfig,
    importInputRef, loadConfig, initialSettings,
  });




  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        height: '100vh',
        maxHeight: '100vh',
        overflow: 'hidden',
        p: 2,
      }}
    >
      <Typography variant="h4" gutterBottom sx={{ flexShrink: 0 }}>
        Settings
      </Typography>

      {saved && (
        <Alert severity="success" sx={{ mb: 2, flexShrink: 0 }}>
          Settings saved successfully!
        </Alert>
      )}
      {importNotice && (
        <Alert severity="info" sx={{ mb: 2, flexShrink: 0 }}>
          {importNotice}
        </Alert>
      )}

      <Paper
        sx={{
          display: 'flex',
          flexDirection: 'column',
          flex: 1,
          minHeight: 0,
          overflow: 'hidden',
        }}
      >
        <Box sx={{ borderBottom: 1, borderColor: 'divider' }}>
          <Tabs
            value={tabValue}
            onChange={(_, newValue) => {
              setTabValue(newValue);
              window.history.replaceState(null, '', `#${TAB_KEYS[newValue]}`);
            }}
            aria-label="settings tabs"
            variant="scrollable"
            scrollButtons="auto"
            allowScrollButtonsMobile
          >
            <Tab label="Library" />
            <Tab label="iTunes Import" />
            <Tab label="Metadata" />
            <Tab label="Dedup" />
            <Tab label="Paths" />
            <Tab label="Performance" />
            <Tab label="Security" />
            <Tab label="API Keys" />
            <Tab label="Plugins" />
            <Tab label="Tools" />
            <Tab label="System Info" />
            <Tab label="Temp Login" />
          </Tabs>
        </Box>

        <TabPanel value={tabValue} index={0}>
          <SettingsGeneral
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
        </TabPanel>

        <TabPanel value={tabValue} index={1}>
          <ITunesImport />
          <ITunesTransfer />
        </TabPanel>

        <TabPanel value={tabValue} index={2}>
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
        </TabPanel>

        <TabPanel value={tabValue} index={3}>
          <DedupSettingsSection config={dedupConfig} onChange={handleDedupChange} />
          <Box sx={{ mt: 4 }}>
            <EmbeddingSettingsSection config={embeddingConfig} onChange={handleEmbeddingChange} />
          </Box>
          <Box sx={{ mt: 4 }}>
            <MetadataScoringSection config={metadataScoringConfig} onChange={handleMetadataScoringChange} />
          </Box>
          {scheduledConfig && (
            <Box sx={{ mt: 4 }}>
              <ScheduledTasksSection config={scheduledConfig} onChange={handleScheduledChange} />
            </Box>
          )}
        </TabPanel>

        <TabPanel value={tabValue} index={4}>
          <PathsSettingsTab
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
        </TabPanel>

        <TabPanel value={tabValue} index={5}>
          <PerformanceSettingsTab settings={settings} handleChange={handleChange} />
        </TabPanel>

        <TabPanel value={tabValue} index={6}>
          <BlockedHashesTab />
        </TabPanel>

        <TabPanel value={tabValue} index={7}>
          <APIKeysTab />
        </TabPanel>

        <TabPanel value={tabValue} index={8}>
          <PluginsTab />
        </TabPanel>

        <TabPanel value={tabValue} index={9}>
          <ToolsSettingsTab />
          <Accordion sx={{ mt: 3 }}>
            <AccordionSummary expandIcon={<ExpandMoreIcon />}>
              <Typography>Advanced: Tools Config</Typography>
            </AccordionSummary>
            <AccordionDetails>
              <TextField
                label="Managed tools directory"
                value={toolsConfig.managed_dir}
                onChange={(e) => handleToolsChange({ managed_dir: e.target.value })}
                fullWidth
                helperText="Directory where managed binaries (Ollama, fpcalc) are downloaded"
                sx={{ mb: 2 }}
              />
              <TextField
                label="Embed queue debounce (ms)"
                type="number"
                value={toolsConfig.embed_queue_debounce_ms}
                onChange={(e) => handleToolsChange({ embed_queue_debounce_ms: Number(e.target.value) })}
                helperText="Milliseconds to wait before draining embed queue"
              />
            </AccordionDetails>
          </Accordion>
        </TabPanel>

        <TabPanel value={tabValue} index={10}>
          <SystemInfoTab />

          <AutoUpdateSection settings={settings} setSettings={setSettings} />

          <MaintenanceSettingsSection config={maintenanceConfig} onChange={handleMaintenanceChange} />

          <Paper
            sx={{
              mt: 4,
              p: 3,
              border: 2,
              borderColor: 'error.main',
              borderRadius: 1,
            }}
          >
            <Typography variant="h6" color="error" gutterBottom>
              Danger Zone
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Permanently delete all data including audiobooks, authors, series,
              settings, and metadata cache. This cannot be undone.
            </Typography>
            <Button
              variant="outlined"
              color="error"
              onClick={() => setFactoryResetStep(1)}
              disabled={factoryResetInProgress}
            >
              Factory Reset
            </Button>
          </Paper>

          {/* Factory Reset Dialog 1: Warning */}
          <Dialog
            open={factoryResetStep === 1}
            onClose={() => setFactoryResetStep(0)}
          >
            <DialogTitle>Factory Reset</DialogTitle>
            <DialogContent>
              <Typography>
                This will permanently delete <strong>ALL</strong> data —
                audiobooks, authors, series, settings, and metadata cache. This
                action cannot be undone.
              </Typography>
              <Typography sx={{ mt: 1 }}>Continue?</Typography>
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setFactoryResetStep(0)}>Cancel</Button>
              <Button
                color="error"
                onClick={() => {
                  setFactoryResetConfirmText('');
                  setFactoryResetStep(2);
                }}
              >
                Continue
              </Button>
            </DialogActions>
          </Dialog>

          {/* Factory Reset Dialog 2: Type RESET */}
          <Dialog
            open={factoryResetStep === 2}
            onClose={() => setFactoryResetStep(0)}
          >
            <DialogTitle>Confirm Factory Reset</DialogTitle>
            <DialogContent>
              <Typography sx={{ mb: 2 }}>
                Type <strong>RESET</strong> to confirm.
              </Typography>
              <TextField
                autoFocus
                fullWidth
                value={factoryResetConfirmText}
                onChange={(e: ChangeEvent<HTMLInputElement>) =>
                  setFactoryResetConfirmText(e.target.value)
                }
                placeholder="Type RESET"
              />
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setFactoryResetStep(0)}>Cancel</Button>
              <Button
                color="error"
                variant="contained"
                disabled={
                  factoryResetConfirmText !== 'RESET' ||
                  factoryResetInProgress
                }
                onClick={async () => {
                  setFactoryResetInProgress(true);
                  try {
                    await api.factoryReset('RESET');
                    localStorage.clear();
                    window.location.href = '/';
                  } catch (err) {
                    setFactoryResetInProgress(false);
                    setFactoryResetStep(0);
                    alert(
                      `Factory reset failed: ${err instanceof Error ? err.message : 'Unknown error'}`
                    );
                  }
                }}
              >
                {factoryResetInProgress ? 'Resetting...' : 'Reset Everything'}
              </Button>
            </DialogActions>
          </Dialog>
        </TabPanel>

        <TabPanel value={tabValue} index={11}>
          <TempLoginTab />
        </TabPanel>

        <Box
          sx={{
            position: 'sticky',
            bottom: 0,
            p: 2,
            display: 'flex',
            gap: 2,
            justifyContent: 'flex-end',
            borderTop: 1,
            borderColor: 'divider',
            bgcolor: 'background.paper',
            zIndex: 10,
            boxShadow: '0 -2px 8px rgba(0,0,0,0.3)',
          }}
        >
          <input
            type="file"
            accept="application/json"
            ref={importInputRef}
            onChange={handleImportFileChange}
            style={{ display: 'none' }}
          />
          <Button
            variant="outlined"
            onClick={handleExportSettings}
            disabled={exportInProgress}
          >
            {exportInProgress ? 'Exporting...' : 'Export Settings'}
          </Button>
          <Button
            variant="outlined"
            onClick={handleImportClick}
            disabled={importInProgress}
          >
            {importInProgress ? 'Importing...' : 'Import Settings'}
          </Button>
          <Button
            variant="outlined"
            startIcon={<RestartAltIcon />}
            onClick={handleReset}
          >
            Reset to Defaults
          </Button>
          <Button
            variant="contained"
            startIcon={<SaveIcon />}
            onClick={handleSave}
            disabled={!configLoaded}
          >
            Save Settings
          </Button>
        </Box>
      </Paper>

      {/* Floating save/cancel panel — visible when there are unsaved changes */}
      {hasUnsavedChanges && (
        <Paper
          elevation={6}
          sx={{
            position: 'fixed',
            bottom: 24,
            right: 24,
            zIndex: 1300,
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            px: 2.5,
            py: 1.5,
            borderRadius: 3,
            bgcolor: 'background.paper',
            boxShadow: 6,
          }}
        >
          <SaveIcon fontSize="small" color="primary" sx={{ mr: 0.5 }} />
          <Button
            size="small"
            onClick={() => {
              if (savedSnapshot) {
                const prev = JSON.parse(savedSnapshot) as SettingsState;
                setSettings(prev);
              }
            }}
          >
            Discard
          </Button>
          <Button
            size="small"
            variant="contained"
            onClick={handleSave}
            disabled={!configLoaded}
          >
            Save
          </Button>
        </Paper>
      )}

      {/* Library Path Browser Dialog */}
      <Dialog
        open={browserOpen}
        onClose={handleBrowserCancel}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle>Browse Server Filesystem</DialogTitle>
        <DialogContent>
          <Typography variant="body2" color="text.secondary" gutterBottom>
            Select the library path where organized audiobooks will be stored.
          </Typography>
          <Box sx={{ mt: 2 }}>
            <ServerFileBrowser
              initialPath={selectedPath || settings.libraryPath}
              onSelect={handleBrowserSelect}
              showFiles={false}
              allowDirSelect={true}
              allowFileSelect={false}
            />
          </Box>
          {selectedPath && (
            <Alert severity="info" sx={{ mt: 2 }}>
              <Typography variant="body2">
                <strong>Selected:</strong> {selectedPath}
              </Typography>
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={handleBrowserCancel}>Cancel</Button>
          <Button
            onClick={handleBrowserConfirm}
            variant="contained"
            disabled={!selectedPath}
          >
            Select Folder
          </Button>
        </DialogActions>
      </Dialog>

      {/* Import Folder Dialog */}
      <Dialog
        open={addFolderDialogOpen}
        onClose={() => setAddFolderDialogOpen(false)}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle>Add Import Path (Watch Location)</DialogTitle>
        <DialogContent>
          <Alert severity="info" sx={{ mb: 2 }}>
            <strong>Import paths</strong> are separate from your main library.
            They are watched for new audiobook files that will be scanned,
            organized, and moved to your library path.
          </Alert>

          {!showFolderBrowser ? (
            <Box>
              <TextField
                autoFocus
                fullWidth
                label="Folder Path"
                value={newFolderPath}
                onChange={(e) => setNewFolderPath(e.target.value)}
                placeholder="/path/to/downloads"
                sx={{ mt: 1 }}
              />
              <Button
                startIcon={<FolderOpenIcon />}
                onClick={() => setShowFolderBrowser(true)}
                sx={{ mt: 2 }}
              >
                Browse Server Filesystem
              </Button>
            </Box>
          ) : (
            <Box>
              <Button
                onClick={() => setShowFolderBrowser(false)}
                sx={{ mb: 2 }}
              >
                ← Back to Manual Entry
              </Button>
              <ServerFileBrowser
                initialPath={newFolderPath || '/'}
                onSelect={handleFolderBrowserSelect}
                showFiles={false}
                allowDirSelect={true}
                allowFileSelect={false}
              />
              {newFolderPath && (
                <Alert severity="success" sx={{ mt: 2 }}>
                  <Typography variant="body2">
                    <strong>Selected:</strong> {newFolderPath}
                  </Typography>
                </Alert>
              )}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => {
              setAddFolderDialogOpen(false);
              setShowFolderBrowser(false);
            }}
          >
            Cancel
          </Button>
          <Button
            onClick={handleAddImportFolder}
            variant="contained"
            disabled={!newFolderPath.trim()}
          >
            Add Path
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={Boolean(cancelScanTarget)}
        onClose={() => setCancelScanTarget(null)}
      >
        <DialogTitle>Cancel Scan</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            Cancel scan for{' '}
            <strong>{cancelScanTarget?.path || 'this folder'}</strong>?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCancelScanTarget(null)}>
            Keep Scanning
          </Button>
          <Button
            color="error"
            variant="contained"
            onClick={handleConfirmCancelScan}
          >
            Cancel Scan
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={Boolean(scanErrorTarget)}
        onClose={() => setScanErrorTarget(null)}
      >
        <DialogTitle>Scan Errors</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            Errors while scanning{' '}
            <strong>{scanErrorTarget?.path || 'this folder'}</strong>:
          </Typography>
          {scanErrorTarget?.errors?.length ? (
            <List dense>
              {scanErrorTarget.errors.map((error, index) => (
                <ListItem key={`${error}-${index}`}>
                  <ListItemText primary={error} />
                </ListItem>
              ))}
            </List>
          ) : (
            <Typography variant="body2" color="text.secondary">
              No errors recorded.
            </Typography>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setScanErrorTarget(null)}>Close</Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={restoreDialogOpen}
        onClose={() => setRestoreDialogOpen(false)}
      >
        <DialogTitle>Restore Backup</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            This will replace the current database with the selected backup.
          </Alert>
          <Typography variant="body2" gutterBottom>
            Restore from{' '}
            <strong>{restoreTarget?.filename || 'selected backup'}</strong>?
          </Typography>
          <FormControlLabel
            control={
              <Switch
                checked={restoreVerify}
                onChange={(e) => setRestoreVerify(e.target.checked)}
              />
            }
            label="Verify backup before restore"
          />
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => setRestoreDialogOpen(false)}
            disabled={restoreInProgress}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            onClick={handleConfirmRestore}
            disabled={restoreInProgress}
          >
            {restoreInProgress ? 'Restoring...' : 'Restore'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={Boolean(deleteBackupTarget)}
        onClose={() => setDeleteBackupTarget(null)}
      >
        <DialogTitle>Delete Backup</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            Delete{' '}
            <strong>{deleteBackupTarget?.filename || 'this backup'}</strong>?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => setDeleteBackupTarget(null)}
            disabled={deleteBackupInProgress}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            color="error"
            onClick={handleConfirmDeleteBackup}
            disabled={deleteBackupInProgress}
          >
            {deleteBackupInProgress ? 'Deleting...' : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={importDialogOpen}
        onClose={() => setImportDialogOpen(false)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Import Settings</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            Importing settings will overwrite your current configuration.
          </Alert>
          <Typography variant="body2" gutterBottom>
            Import from <strong>{importFileName || 'selected file'}</strong>?
          </Typography>
          {importKeys.length > 0 && (
            <List dense>
              {importKeys.slice(0, 12).map((key) => (
                <ListItem key={key}>
                  <ListItemText primary={key} />
                </ListItem>
              ))}
              {importKeys.length > 12 && (
                <ListItem>
                  <ListItemText
                    primary={`+${importKeys.length - 12} more fields`}
                  />
                </ListItem>
              )}
            </List>
          )}
        </DialogContent>
        <DialogActions>
          <Button
            onClick={() => setImportDialogOpen(false)}
            disabled={importInProgress}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            onClick={handleConfirmImport}
            disabled={importInProgress}
          >
            {importInProgress ? 'Importing...' : 'Import Settings'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={blocker.state === 'blocked'}
        onClose={handleCancelNavigation}
      >
        <DialogTitle>Unsaved Changes</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            You have unsaved changes. Save before leaving?
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={handleCancelNavigation}>Cancel</Button>
          <Button onClick={handleDiscardNavigation}>Discard</Button>
          <Button variant="contained" onClick={handleSaveAndNavigate}>
            Save
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
