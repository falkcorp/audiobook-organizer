// file: web/src/components/AnnouncementBanner.tsx
// version: 1.3.1
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-11-07

// Moved to @mediacore/ui-kit (F9-05). The banner takes its announcements, its
// storage key and a navigate callback as props; this wrapper does the fetching
// and the routing, and keeps the legacy dismissed-announcements key until the
// shell adoption (F9-03).

import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router';
import { AnnouncementBanner as UiKitAnnouncementBanner, type Announcement } from '@mediacore/ui-kit';
import { STORAGE_KEYS } from '../lib/storageKeys';
import { apiFetch } from '../utils/apiFetch';
import { unwrapData } from '../utils/apiResponse';

export function AnnouncementBanner() {
  const navigate = useNavigate();
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);

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

  return (
    <UiKitAnnouncementBanner
      announcements={announcements}
      storageKey={STORAGE_KEYS.DISMISSED_ANNOUNCEMENTS}
      onNavigate={navigate}
    />
  );
}
