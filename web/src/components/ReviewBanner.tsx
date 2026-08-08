// file: web/src/components/ReviewBanner.tsx
// version: 1.0.1
// guid: 9b2e7d41-6c30-4a58-8f19-2d7a5e0c3b64
// last-edited: 2026-08-07

import { useNavigate } from 'react-router-dom';
import { Alert, Box } from '@mui/material';
import FactCheckIcon from '@mui/icons-material/FactCheck';
import { useReviewStore } from '../stores/useReviewStore';

/**
 * ReviewBanner is the single, glanceable "You have N items to review" banner
 * (decision #2 — one aggregate number, breakdown lives inside the /review page).
 *
 * Rendered in MainLayout above every route's content, so it shows everywhere
 * (unlike the Dashboard-only AnnouncementBanner). Hidden when the count is 0;
 * clicking navigates to /review. The count is polled by useReviewStore, which
 * App.tsx starts on auth-ready.
 */
export function ReviewBanner() {
  const navigate = useNavigate();
  const count = useReviewStore((s) => s.count);

  if (count <= 0) return null;

  return (
    <Box sx={{ mb: 2 }}>
      <Alert
        severity="info"
        icon={<FactCheckIcon fontSize="inherit" />}
        sx={{ cursor: 'pointer' }}
        onClick={() => navigate('/review')}
      >
        You have {count} item{count === 1 ? '' : 's'} to review
      </Alert>
    </Box>
  );
}
