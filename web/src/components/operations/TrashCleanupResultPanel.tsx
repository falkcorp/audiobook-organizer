// file: web/src/components/operations/TrashCleanupResultPanel.tsx
// version: 1.5.0
// guid: 142d4823-4f1c-4c19-95fe-7a3c0f9f05b4
// last-edited: 2026-10-29

import { useEffect, useState, type ReactNode } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { Box, Button, Collapse, Link, Typography } from '@mui/material';
import { getOperationResult } from '../../services/api';
import {
  cleanupBookIds,
  cleanupVersionsLink,
  parseTrashCleanupResult,
  type KeptSweepFile,
  type TrashCleanupResult,
  type TrashCleanupSet,
} from '../../utils/trashCleanupResult';

/** How many books and file paths are listed before the rest is left to the versions page. */
const LIST_CAP = 10;

/** How many sweep files are listed before the rest is left to the operation's log (one line each). */
const SWEEP_LIST_CAP = 50;

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

interface PathListProps {
  paths: string[];
  opId: string;
  /** the group the "and more" link opens */
  set: TrashCleanupSet;
  /** the button text for n paths */
  label: (n: number) => string;
}

/** A group's paths (files kept on disk, or the books' own paths), behind a button that shows them. */
function PathList({ paths, opId, set, label }: PathListProps) {
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
        {label(paths.length)}
      </Button>
      <Collapse in={open} unmountOnExit>
        <Box sx={{ pl: 1, fontFamily: 'monospace', fontSize: '0.75rem', wordBreak: 'break-all' }}>
          {shown.map((p) => (
            <div key={p}>{p}</div>
          ))}
          {more > 0 && (
            <Link component={RouterLink} to={cleanupVersionsLink(opId, set)}>
              and {more.toLocaleString()} more: see the versions
            </Link>
          )}
        </Box>
      </Collapse>
    </Box>
  );
}

interface SweepFileListProps {
  files: KeptSweepFile[];
  /** the button text for n files */
  label: (n: number) => string;
  /** what the reason says, in front of it */
  reasonLead: string;
  testId: string;
}

/**
 * The kept-file sweep's files behind a button: each path with its version, a
 * link to its book and the reason. The list is the owner's evidence for flipping
 * kept_sweep_apply_enabled, so it shows more than the other path lists do.
 */
function SweepFileList({ files, label, reasonLead, testId }: SweepFileListProps) {
  const [open, setOpen] = useState(false);
  if (files.length === 0) return null;
  const shown = files.slice(0, SWEEP_LIST_CAP);
  const more = files.length - shown.length;
  return (
    <Box sx={{ mb: 0.5 }} data-testid={testId}>
      <Button
        size="small"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        aria-expanded={open}
      >
        {label(files.length)}
      </Button>
      <Collapse in={open} unmountOnExit>
        <Box sx={{ pl: 1 }}>
          {shown.map((f) => (
            <Box key={`${f.version_id}:${f.path}`} sx={{ mb: 0.5 }}>
              <Box sx={{ fontFamily: 'monospace', fontSize: '0.75rem', wordBreak: 'break-all' }}>
                {f.path}
              </Box>
              <Typography variant="caption" component="div" sx={{ color: 'text.secondary' }}>
                version {f.version_id.slice(0, 12)}
                {f.book_id !== '' && (
                  <>
                    {', book '}
                    <Link component={RouterLink} to={`/library/${encodeURIComponent(f.book_id)}`}>
                      {f.book_id.slice(0, 12)}
                    </Link>
                  </>
                )}
                {f.reason !== '' && ` (${reasonLead}: ${f.reason})`}
              </Typography>
            </Box>
          ))}
          {more > 0 && (
            <Typography variant="caption" component="div">
              and {more.toLocaleString()} more: the operation&apos;s log below lists every file.
            </Typography>
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
          <PathList
            paths={result.kept_file_paths}
            opId={opId}
            set="kept"
            label={(n) => `${n.toLocaleString()} ${plural(n, 'file', 'files')} kept on disk`}
          />
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
      {result.cannot_tell > 0 && (
        <SetLine opId={opId} set="cannot_tell" count={result.cannot_tell} result={result}>
          {plural(result.cannot_tell, 'was', 'were')} left alone: the files left are not there and
          nothing shows they were removed (a drive that is down looks the same). Nothing was
          deleted; {plural(result.cannot_tell, 'it is', 'they are')} tried again next run, and a
          purge that gets nowhere three times becomes purge failed, which can be hard-deleted.
        </SetLine>
      )}
      {result.nothing_served > 0 && (
        <>
          <SetLine opId={opId} set="nothing_served" count={result.nothing_served} result={result}>
            {plural(result.nothing_served, 'was', 'were')} left in the trash: the book serves
            nothing that exists on disk, so there is nothing to check{' '}
            {plural(result.nothing_served, 'its files', 'their files')} against. Nothing was
            deleted. Fix the book&apos;s path, or restore the version; until then{' '}
            {plural(result.nothing_served, 'it is', 'they are')} listed again every run.
          </SetLine>
          <PathList
            paths={result.nothing_served_file_paths}
            opId={opId}
            set="nothing_served"
            label={(n) => `${n.toLocaleString()} ${plural(n, 'book path', 'book paths')} to fix`}
          />
        </>
      )}
      {result.served_kept > 0 && (
        <SetLine opId={opId} set="served_kept" count={result.served_kept} result={result}>
          {plural(result.served_kept, 'was', 'were')} kept in the trash: the book still plays{' '}
          {plural(result.served_kept, 'its file', 'their files')}. Nothing was deleted;{' '}
          {plural(result.served_kept, 'it is', 'they are')} tried again next run. Restore the
          version, or change what the book plays, to let the cleanup finish.
        </SetLine>
      )}
      {result.swap_incomplete > 0 && (
        <>
          <SetLine opId={opId} set="swap_incomplete" count={result.swap_incomplete} result={result}>
            {plural(result.swap_incomplete, 'is', 'are')} in a version swap that did not finish: the
            book has no active version until the swap is resumed. The cleanup only lists{' '}
            {plural(result.swap_incomplete, 'it', 'them')} and moved nothing; resume each book from
            Version Management, Unfinished swaps.{' '}
            {plural(result.swap_incomplete, 'It is', 'They are')} listed again every run until then.
          </SetLine>
          <PathList
            paths={result.swap_incomplete_file_paths}
            opId={opId}
            set="swap_incomplete"
            label={(n) => `${n.toLocaleString()} ${plural(n, 'book path', 'book paths')} to repair`}
          />
        </>
      )}
      {(result.kept_sweep_report_only ||
        result.kept_sweep_removed > 0 ||
        result.kept_sweep_gone > 0 ||
        result.kept_sweep_escalated > 0) && (
        <Box data-testid="trash-cleanup-kept-sweep" sx={{ mb: 0.5 }}>
          {result.kept_sweep_report_only ? (
            <Typography variant="body2" data-testid="trash-cleanup-kept-sweep-report">
              Kept-file sweep, report only (nothing was changed): it would remove{' '}
              {result.kept_sweep_would_remove.toLocaleString()}{' '}
              {plural(result.kept_sweep_would_remove, 'file', 'files')} and drop the notes of{' '}
              {result.kept_sweep_would_drop.toLocaleString()}{' '}
              {plural(result.kept_sweep_would_drop, 'file', 'files')} that{' '}
              {plural(result.kept_sweep_would_drop, 'is', 'are')} already gone.{' '}
              {result.kept_sweep_still_kept.toLocaleString()} still owned,{' '}
              {result.kept_sweep_kept_on_error.toLocaleString()} could not be decided. Read the
              list, then turn on kept_sweep_apply_enabled to let it act.
            </Typography>
          ) : (
            <Typography variant="body2" data-testid="trash-cleanup-kept-sweep-applied">
              Kept-file sweep: removed {result.kept_sweep_removed.toLocaleString()}{' '}
              {plural(result.kept_sweep_removed, 'file', 'files')} nothing names any more and
              dropped {result.kept_sweep_gone.toLocaleString()}{' '}
              {plural(result.kept_sweep_gone, 'note', 'notes')} of files already gone.
            </Typography>
          )}
          <SweepFileList
            files={result.kept_sweep_would_remove_files}
            label={(n) => `${n.toLocaleString()} ${plural(n, 'file', 'files')} it would remove`}
            reasonLead="kept because"
            testId="trash-cleanup-kept-sweep-would-remove"
          />
          <SweepFileList
            files={result.kept_sweep_would_drop_files}
            label={(n) =>
              `${n.toLocaleString()} ${plural(n, 'note', 'notes')} of already-gone files it would drop`
            }
            reasonLead="kept because"
            testId="trash-cleanup-kept-sweep-would-drop"
          />
          {result.kept_sweep_escalated > 0 && (
            <>
              <Typography
                variant="body2"
                sx={{ color: 'warning.main' }}
                data-testid="trash-cleanup-kept-sweep-escalated"
              >
                {result.kept_sweep_escalated.toLocaleString()}{' '}
                {plural(result.kept_sweep_escalated, 'version', 'versions')} became purge failed:
                the sweep could not settle a kept file several nights in a row. See{' '}
                {plural(result.kept_sweep_escalated, 'it', 'them')} on the trashed versions page;
                Purge now retries one, hard delete drops it.
              </Typography>
              <SweepFileList
                files={result.kept_sweep_escalated_files}
                label={(n) =>
                  `${n.toLocaleString()} ${plural(n, 'file', 'files')} it could not settle`
                }
                reasonLead="why"
                testId="trash-cleanup-kept-sweep-escalated-files"
              />
            </>
          )}
        </Box>
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
