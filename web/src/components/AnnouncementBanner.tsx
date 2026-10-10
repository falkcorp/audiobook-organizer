// file: web/src/components/AnnouncementBanner.tsx
// version: 1.3.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-10

import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, AlertTitle, Box, IconButton, Collapse } from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import { STORAGE_KEYS } from '../lib/storageKeys';
import { apiFetch } from '../utils/apiFetch';
import { unwrapData } from '../utils/apiResponse';

interface Announcement {
  id: string;
  severity: 'info' | 'warning' | 'error';
  message: string;
  link?: string;
}

function getDismissed(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEYS.DISMISSED_ANNOUNCEMENTS);
    return raw ? new Set(JSON.parse(raw)) : new Set();
  } catch {
    return new Set();
  }
}

function dismissAnnouncement(id: string) {
  const dismissed = getDismissed();
  dismissed.add(id);
  localStorage.setItem(STORAGE_KEYS.DISMISSED_ANNOUNCEMENTS, JSON.stringify([...dismissed]));
}

export function AnnouncementBanner() {
  const navigate = useNavigate();
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [dismissed, setDismissed] = useState<Set<string>>(getDismissed);

  const fetchAnnouncements = useCallback(async () => {
    try {
      const response = await apiFetch('/api/v1/system/announcements');
      if (!response.ok) return;
      const data = unwrapData<{ announcements?: Announcement[] }>(await response.json());
      setAnnouncements(data.announcements || []);
    } catch {
      // Silently fail - announcements are non-critical
    }
  }, []);

  useEffect(() => {
    fetchAnnouncements();
  }, [fetchAnnouncements]);

  const handleDismiss = (id: string) => {
    dismissAnnouncement(id);
    setDismissed((prev) => new Set([...prev, id]));
  };

  const visible = announcements.filter((a) => !dismissed.has(a.id));

  if (visible.length === 0) return null;

  return (
    <Box sx={{ mb: 2 }}>
      {visible.map((announcement) => (
        <Collapse key={announcement.id} in>
          <Alert
            severity={announcement.severity}
            sx={{
              mb: 1,
              cursor: announcement.link ? 'pointer' : 'default',
            }}
            onClick={() => {
              if (announcement.link) navigate(announcement.link);
            }}
            action={
              <IconButton
                size="small"
                onClick={(e) => {
                  e.stopPropagation();
                  handleDismiss(announcement.id);
                }}
              >
                <CloseIcon fontSize="small" />
              </IconButton>
            }
          >
            <AlertTitle>
              {announcement.severity === 'error'
                ? 'Action Required'
                : announcement.severity === 'warning'
                  ? 'Attention'
                  : 'Info'}
            </AlertTitle>
            {announcement.message}
          </Alert>
        </Collapse>
      ))}
    </Box>
  );
}
