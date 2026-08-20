// file: web/src/pages/BookDedup.tsx
// version: 3.41.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-book0dedup02
// last-edited: 2026-08-20

import { useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Box, Typography, Tabs, Tab, Badge } from '@mui/material';
import AutoAwesomeIcon from '@mui/icons-material/AutoAwesome';
import BuildIcon from '@mui/icons-material/Build';
import FingerprintIcon from '@mui/icons-material/Fingerprint';
import GraphicEqIcon from '@mui/icons-material/GraphicEq';
import CallSplitIcon from '@mui/icons-material/CallSplit';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import PersonIcon from '@mui/icons-material/Person';
import ListIcon from '@mui/icons-material/List';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import { DedupBookTab } from '../components/dedup/DedupBookTab';
import { BookDedupScanTab } from '../components/dedup/DedupAdvancedScanTab';
import { AuthorDedupTab } from '../components/dedup/DedupAuthorTab';
import { SeriesDedupTab } from '../components/dedup/DedupSeriesTab';
import { ReconcileTab } from '../components/dedup/DedupReconcileTab';
import { DedupSplitBookTab } from '../components/dedup/DedupSplitBookTab';
import { AIReviewTab } from '../components/dedup/DedupAIReviewTab';
import { EmbeddingDedupTab } from '../components/dedup/DedupEmbeddingTab';
import { AcousticDedupTab } from '../components/dedup/DedupAcousticTab';

// ---- Main Dedup Page ----
const TAB_NAMES = [
  'books',
  'book-duplicates',
  'authors',
  'series',
  'ai',
  'reconcile',
  'embedding',
  'acoustic',
  'split-books',
] as const;

export function BookDedup() {
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = useMemo(() => {
    const t = searchParams.get('tab');
    const idx = TAB_NAMES.indexOf(t as (typeof TAB_NAMES)[number]);
    return idx >= 0 ? idx : 0;
  }, [searchParams]);

  const setTab = (v: number) => {
    setSearchParams({ tab: TAB_NAMES[v] || 'books' }, { replace: true });
  };

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2, gap: 2 }}>
        <Typography variant="h5">Deduplication</Typography>
      </Box>

      {/* These are the dedup tools, not "the legacy view". The toggle that
          framed them that way is gone with UnifiedDedupTab, whose job -- book-pair
          duplicate candidates -- moved to the dupes lane of /review. Nothing else
          here overlaps it: authors, series, split-books, reconcile, AI review,
          embedding clusters and AcoustID are separate domains the workspace does
          not touch. */}
      <Tabs
        value={tab}
        onChange={(_, v) => setTab(v)}
        variant="scrollable"
        scrollButtons="auto"
        allowScrollButtonsMobile
        sx={{ mb: 3, borderBottom: 1, borderColor: 'divider' }}
      >
        <Tab
          icon={
            <Badge color="default">
              <MenuBookIcon />
            </Badge>
          }
          label="Version Groups"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <ContentCopyIcon />
            </Badge>
          }
          label="Duplicate Scan"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <PersonIcon />
            </Badge>
          }
          label="Authors"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <ListIcon />
            </Badge>
          }
          label="Series"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <AutoAwesomeIcon />
            </Badge>
          }
          label="AI Review"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <BuildIcon />
            </Badge>
          }
          label="Reconcile"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <FingerprintIcon />
            </Badge>
          }
          label="Embedding"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <GraphicEqIcon />
            </Badge>
          }
          label="Acoustic"
          iconPosition="start"
        />
        <Tab
          icon={
            <Badge color="default">
              <CallSplitIcon />
            </Badge>
          }
          label="Split Books"
          iconPosition="start"
        />
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
    </Box>
  );
}
