// file: web/src/components/operations/TrashCleanupResultPanel.tsx
// version: 1.0.1
// guid: 142d4823-4f1c-4c19-95fe-7a3c0f9f05b4
// last-edited: 2026-10-27

import { useEffect, useState, type ReactNode } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { Box, Button, Collapse, Link, Typography } from '@mui/material';
import { getOperationResult } from '../../services/api';
import {
  cleanupBookIds,
  cleanupVersionsLink,
  parseTrashCleanupResult,
  type TrashCleanupResult,
  type TrashCleanupSet,
} from '../../utils/trashCleanupResult';

/** How many books and file paths are listed before the rest is left to the versions page. */
const LIST_CAP = 10;

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

interface SetLineProps {
  opId: string;
  set: TrashCleanupSet;
  count: number;
  /** the sentence the count is part of, after the linked count */
  children: ReactNode;
  result: TrashCleanupResult;
}

/**
 * One count of the result, as a link to the Version Management page narrowed
 * to its versions, followed by the books behind it (each a link to the book).
 */
function SetLine({ opId, set, count, children, result }: SetLineProps) {
  const link = cleanupVersionsLink(opId, set);
  const books = cleanupBookIds(result, set);
  const shown = books.slice(0, LIST_CAP);
  const more = books.length - shown.length;
  return (
    <Box data-testid={`trash-cleanup-${set}`} sx={{ mb: 0.5 }}>
      <Typography variant="body2" component="span">
        <Link component={RouterLink} to={link}>
          {count.toLocaleString()} {plural(count, 'version', 'versions')}
        </Link>{' '}
        {children}
      </Typography>
      {shown.length > 0 && (
        <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
          {plural(books.length, 'Book', 'Books')}:{' '}
          {shown.map((id, i) => (
            <span key={id}>
              {i > 0 && ', '}
              <Link component={RouterLink} to={`/library/${encodeURIComponent(id)}`}>
                {id.slice(0, 12)}
              </Link>
            </span>
          ))}
          {more > 0 && (
            <>
              {' '}
              and{' '}
              <Link component={RouterLink} to={link}>
                {more.toLocaleString()} more
              </Link>
            </>
          )}
        </Typography>
      )}
    </Box>
  );
}

/** The files kept on disk, behind a button that shows their paths. */
function KeptPaths({ paths, opId }: { paths: string[]; opId: string }) {
  const [open, setOpen] = useState(false);
  if (paths.length === 0) return null;
  const shown = paths.slice(0, LIST_CAP);
  const more = paths.length - shown.length;
  return (
    <Box sx={{ mb: 0.5 }}>
      <Button
        size="small"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        aria-expanded={open}
      >
        {paths.length.toLocaleString()} {plural(paths.length, 'file', 'files')} kept on disk
      </Button>
      <Collapse in={open} unmountOnExit>
        <Box sx={{ pl: 1, fontFamily: 'monospace', fontSize: '0.75rem', wordBreak: 'break-all' }}>
          {shown.map((p) => (
            <div key={p}>{p}</div>
          ))}
          {more > 0 && (
            <Link component={RouterLink} to={cleanupVersionsLink(opId, 'kept')}>
              and {more.toLocaleString()} more: see the versions
            </Link>
          )}
        </Box>
      </Collapse>
    </Box>
  );
}

/**
 * What a finished trash cleanup run did, read from the result it stored. Every
 * count that points at versions links to the Version Management page narrowed
 * to them; the books behind a count link to the book. Renders nothing while the
 * result loads or when the run stored none (the log below still tells the story).
 */
export default function TrashCleanupResultPanel({ opId }: { opId: string }) {
  // Kept with the op it was read for, so a row that switches to another run
  // never shows the previous run's counts while the new result loads.
  const [read, setRead] = useState<{ opId: string; result: TrashCleanupResult | null } | null>(
    null
  );

  useEffect(() => {
    let cancelled = false;
    getOperationResult(opId)
      .then(({ result_data }) => {
        if (!cancelled) setRead({ opId, result: parseTrashCleanupResult(result_data) });
      })
      .catch(() => {
        // No stored result (a run from before it was stored, or still running):
        // the operation's log is the record then.
        if (!cancelled) setRead({ opId, result: null });
      });
    return () => {
      cancelled = true;
    };
  }, [opId]);

  const result = read !== null && read.opId === opId ? read.result : null;
  if (!result) return null;

  return (
    <Box data-testid="trash-cleanup-result" sx={{ mt: 1 }} onClick={(e) => e.stopPropagation()}>
      <Typography variant="body2" sx={{ mb: 0.5 }}>
        Purged {result.purged.toLocaleString()} {plural(result.purged, 'version', 'versions')}.
        {result.failed > 0 &&
          ` ${result.failed.toLocaleString()} could not be purged (the log below says why for each).`}
      </Typography>
      {result.kept_files > 0 && (
        <>
          <SetLine opId={opId} set="kept" count={result.kept_files} result={result}>
            {plural(result.kept_files, 'was', 'were')} purged with files left on disk, because
            another record or book still names them.
          </SetLine>
          <KeptPaths paths={result.kept_file_paths} opId={opId} />
        </>
      )}
      {result.needs_attention > 0 && (
        <SetLine opId={opId} set="needs_attention" count={result.needs_attention} result={result}>
          {plural(result.needs_attention, 'needs', 'need')} attention: a file could not be removed
          (permission or a read-only filesystem), so the cleanup does not retry{' '}
          {plural(result.needs_attention, 'it', 'them')}. Purge now retries one; hard delete drops
          it.
        </SetLine>
      )}
      {result.stopped_for_merge && (
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>
          Stopped early: a merge or combine was running the whole time the cleanup waited, so the
          remaining versions are left for the next run. Nothing failed.
        </Typography>
      )}
      {result.stopped_for_merge_repeatedly && (
        <Typography
          variant="body2"
          sx={{ color: 'warning.main' }}
          data-testid="trash-cleanup-merge-stuck"
        >
          The cleanup has now stopped for this reason {result.stopped_for_merge_nights} nights in a
          row (a merge or combine was running), so the trash is not being purged. Check what is
          holding the library busy around this time.
        </Typography>
      )}
    </Box>
  );
}
