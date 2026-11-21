// file: core/web/packages/ui-kit/src/components/AnnouncementBanner.tsx
// version: 0.1.0
// guid: afc172d7-dbf8-446f-badf-9f9663ae2683
// last-edited: 2026-11-21

import { useCallback, useState } from 'react';
import { Alert, AlertTitle, Box, Collapse, IconButton } from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';

export interface Announcement {
  id: string;
  severity: 'info' | 'warning' | 'error';
  message: string;
  /** Where a click on the banner goes, through `onNavigate`. */
  link?: string;
}

export interface AnnouncementBannerProps {
  /** The announcements to show, as the host fetched them. The component does no I/O. */
  announcements: readonly Announcement[];
  /**
   * The localStorage key the dismissed ids are kept under, used as given (the
   * host passes a namespaced key; ui-kit has no app ID to prefix one with).
   */
  storageKey: string;
  /** Called with `link` when a banner that has one is clicked. Without it a click does nothing. */
  onNavigate?: (link: string) => void;
}

function readDismissed(storageKey: string): Set<string> {
  try {
    const raw = localStorage.getItem(storageKey);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : []);
  } catch {
    return new Set();
  }
}

function writeDismissed(storageKey: string, ids: ReadonlySet<string>) {
  try {
    localStorage.setItem(storageKey, JSON.stringify([...ids]));
  } catch {
    // Storage unavailable: the dismissal lasts until the page is left.
  }
}

const TITLES = { error: 'Action Required', warning: 'Attention', info: 'Info' } as const;

export function AnnouncementBanner({ announcements, storageKey, onNavigate }: AnnouncementBannerProps) {
  const [dismissed, setDismissed] = useState<Set<string>>(() => readDismissed(storageKey));

  const handleDismiss = useCallback(
    (id: string) => {
      // Merge with what storage holds now, so a second tab's dismissals are kept.
      const next = readDismissed(storageKey);
      for (const d of dismissed) next.add(d);
      next.add(id);
      writeDismissed(storageKey, next);
      setDismissed(next);
    },
    [dismissed, storageKey],
  );

  const visible = announcements.filter((a) => !dismissed.has(a.id));
  if (visible.length === 0) return null;

  return (
    <Box sx={{ mb: 2 }}>
      {visible.map((announcement) => (
        <Collapse key={announcement.id} in>
          <Alert
            severity={announcement.severity}
            sx={{ mb: 1, cursor: announcement.link && onNavigate ? 'pointer' : 'default' }}
            onClick={() => {
              if (announcement.link) onNavigate?.(announcement.link);
            }}
            action={
              <IconButton
                size="small"
                aria-label="Dismiss"
                onClick={(e) => {
                  e.stopPropagation();
                  handleDismiss(announcement.id);
                }}
              >
                <CloseIcon fontSize="small" />
              </IconButton>
            }
          >
            <AlertTitle>{TITLES[announcement.severity]}</AlertTitle>
            {announcement.message}
          </Alert>
        </Collapse>
      ))}
    </Box>
  );
}
