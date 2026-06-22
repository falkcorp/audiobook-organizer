// file: web/src/pages/BookDedup.tsx
// version: 3.40.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-book0dedup02
// last-edited: 2026-06-22

import { useState, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Box,
  Typography,
  Tooltip,
  Button,
  Tabs,
  Tab,
  Badge,
} from '@mui/material';
import AutoAwesomeIcon from '@mui/icons-material/AutoAwesome';
import BuildIcon from '@mui/icons-material/Build';
import FingerprintIcon from '@mui/icons-material/Fingerprint';
import GraphicEqIcon from '@mui/icons-material/GraphicEq';
import CallSplitIcon from '@mui/icons-material/CallSplit';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import PersonIcon from '@mui/icons-material/Person';
import ListIcon from '@mui/icons-material/List';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import ViewListIcon from '@mui/icons-material/ViewList';
import { DedupBookTab } from '../components/dedup/DedupBookTab';
import { BookDedupScanTab } from '../components/dedup/DedupAdvancedScanTab';
import { AuthorDedupTab } from '../components/dedup/DedupAuthorTab';
import { SeriesDedupTab } from '../components/dedup/DedupSeriesTab';
import { ReconcileTab } from '../components/dedup/DedupReconcileTab';
import { DedupSplitBookTab } from '../components/dedup/DedupSplitBookTab';
import { UnifiedDedupTab } from '../components/dedup/UnifiedDedupTab';
import { AIReviewTab } from '../components/dedup/DedupAIReviewTab';
import { EmbeddingDedupTab } from '../components/dedup/DedupEmbeddingTab';
import { AcousticDedupTab } from '../components/dedup/DedupAcousticTab';

// LSH backfill completed 2026-06-11 (275K files indexed, 15K dedup pairs scored).
// Feature gate removed — unified dedup tab is now always enabled.
function isUnifiedDedupEnabled(): boolean {
  return true;
}

// ---- Main Dedup Page ----
const TAB_NAMES = ['books', 'book-duplicates', 'authors', 'series', 'ai', 'reconcile', 'embedding', 'acoustic', 'split-books'] as const;

export function BookDedup() {
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = useMemo(() => {
    const t = searchParams.get('tab');
    const idx = TAB_NAMES.indexOf(t as typeof TAB_NAMES[number]);
    return idx >= 0 ? idx : 0;
  }, [searchParams]);

  // T017: unified dedup tab feature flag + legacy toggle.
  // Feature is default-off; enable via localStorage or VITE_ENABLE_UNIFIED_DEDUP.
  const unifiedEnabled = isUnifiedDedupEnabled();
  // When unified is enabled the user can still opt into the legacy tabs via
  // the toggle. State is persisted in sessionStorage so it doesn't reset on
  // every navigation but resets on a new session (matches "one release" semantics).
  const [showLegacy, setShowLegacy] = useState<boolean>(() => {
    try {
      return sessionStorage.getItem('dedup_show_legacy') === '1';
    } catch {
      return false;
    }
  });
  const handleToggleLegacy = () => {
    setShowLegacy((prev) => {
      const next = !prev;
      try { sessionStorage.setItem('dedup_show_legacy', next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  };

  const setTab = (v: number) => {
    setSearchParams({ tab: TAB_NAMES[v] || 'books' }, { replace: true });
  };

  // When the unified tab is active (enabled + not toggled to legacy),
  // show only the unified surface with a "Show legacy" toggle button.
  const showUnified = unifiedEnabled && !showLegacy;

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2, gap: 2 }}>
        <Typography variant="h5">Deduplication</Typography>
        {unifiedEnabled && (
          <Tooltip
            title={
              showLegacy
                ? 'Switch back to the new unified view'
                : 'Show legacy tab view (Books / Scan / Acoustic tabs)'
            }
          >
            <Button
              size="small"
              variant="outlined"
              color="inherit"
              startIcon={<ViewListIcon />}
              onClick={handleToggleLegacy}
              data-testid="legacy-toggle-btn"
            >
              {showLegacy ? 'New View' : 'Legacy View'}
            </Button>
          </Tooltip>
        )}
      </Box>

      {/* T017 Unified Dedup Tab — shown when feature is enabled and legacy toggle is off */}
      {showUnified && (
        <Box data-testid="unified-dedup-tab-wrapper">
          <UnifiedDedupTab />
        </Box>
      )}

      {/* Legacy tabs — kept mounted when legacy toggle is on or feature is disabled */}
      {!showUnified && (
        <>
          <Tabs value={tab} onChange={(_, v) => setTab(v)} variant="scrollable" scrollButtons="auto" allowScrollButtonsMobile sx={{ mb: 3, borderBottom: 1, borderColor: 'divider' }}>
            <Tab icon={<Badge color="default"><MenuBookIcon /></Badge>} label="Version Groups" iconPosition="start" />
            <Tab icon={<Badge color="default"><ContentCopyIcon /></Badge>} label="Duplicate Scan" iconPosition="start" />
            <Tab icon={<Badge color="default"><PersonIcon /></Badge>} label="Authors" iconPosition="start" />
            <Tab icon={<Badge color="default"><ListIcon /></Badge>} label="Series" iconPosition="start" />
            <Tab icon={<Badge color="default"><AutoAwesomeIcon /></Badge>} label="AI Review" iconPosition="start" />
            <Tab icon={<Badge color="default"><BuildIcon /></Badge>} label="Reconcile" iconPosition="start" />
            <Tab icon={<Badge color="default"><FingerprintIcon /></Badge>} label="Embedding" iconPosition="start" />
            <Tab icon={<Badge color="default"><GraphicEqIcon /></Badge>} label="Acoustic" iconPosition="start" />
            <Tab icon={<Badge color="default"><CallSplitIcon /></Badge>} label="Split Books" iconPosition="start" />
          </Tabs>

          {tab === 0 && <DedupBookTab />}
          {tab === 1 && <BookDedupScanTab />}
          {tab === 2 && <AuthorDedupTab />}
          {tab === 3 && <SeriesDedupTab />}
          {tab === 4 && <AIReviewTab />}
          {tab === 5 && <ReconcileTab />}
          {tab === 6 && <EmbeddingDedupTab />}
          {tab === 7 && <AcousticDedupTab />}
          {tab === 8 && <DedupSplitBookTab />}
        </>
      )}
    </Box>
  );
}
