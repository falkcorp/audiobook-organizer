// file: web/src/components/FingerprintVisualsColumn.tsx
// version: 1.1.1
// guid: f1a2b3c4-d5e6-7890-abcd-ef1234567890
// last-edited: 2026-08-19

import React from 'react';
import { Box, IconButton, Tooltip } from '@mui/material';
import CompareArrowsIcon from '@mui/icons-material/CompareArrows';
import { useNavigate } from 'react-router-dom';

interface FingerprintVisualProps {
  book: any; // Book object from API
}

// Extract fingerprint segments (AcoustIDSeg0–Seg6) from book metadata
function getFingerprints(book: any): number[] {
  const segments = [
    book.acoustid_seg0,
    book.acoustid_seg1,
    book.acoustid_seg2,
    book.acoustid_seg3,
    book.acoustid_seg4,
    book.acoustid_seg5,
    book.acoustid_seg6,
  ].filter((s) => s !== undefined && s !== null);

  // If segments are present, return as numbers (confidence/hash values)
  // For now, we'll normalize them to 0-100 range for visualization
  return segments.length > 0
    ? segments.map((s) => Math.min(100, (parseInt(s, 16) || 0) % 100))
    : [];
}

export const FingerprintWaveform: React.FC<FingerprintVisualProps> = ({ book }) => {
  const fingerprints = getFingerprints(book);

  if (fingerprints.length === 0) {
    return (
      <Tooltip title="No fingerprint data">
        <Box
          sx={{
            width: '100%',
            height: 30,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          —
        </Box>
      </Tooltip>
    );
  }

  return (
    <Tooltip title={`${fingerprints.length} segments`}>
      <Box
        sx={{
          width: '100%',
          height: 30,
          display: 'flex',
          gap: 1,
          alignItems: 'flex-end',
          padding: '4px 0',
        }}
      >
        {fingerprints.map((height, idx) => (
          <Box
            key={idx}
            sx={{
              flex: 1,
              height: `${height}%`,
              backgroundColor: `hsl(${(idx / fingerprints.length) * 240}, 70%, 60%)`,
              minHeight: 2,
              borderRadius: '2px',
            }}
          />
        ))}
      </Box>
    </Tooltip>
  );
};

// CompareInDedupButton renders a small icon button that deep-links to the
// unified dedup tab filtered to this book's candidates.
// URL: /dedup?tab=unified&book=<book.id>  (tab param consumed by BookDedup)
export const CompareInDedupButton: React.FC<FingerprintVisualProps> = ({ book }) => {
  const navigate = useNavigate();
  if (!book?.id) return null;
  return (
    <Tooltip title="Compare in Dedup — find candidates for this book">
      <IconButton
        size="small"
        onClick={() => navigate(`/dedup?book=${encodeURIComponent(book.id)}`)}
        aria-label="Compare in Dedup"
        data-testid="compare-in-dedup-btn"
      >
        <CompareArrowsIcon fontSize="small" />
      </IconButton>
    </Tooltip>
  );
};

export const FingerprintSpectrogram: React.FC<FingerprintVisualProps> = ({ book }) => {
  const fingerprints = getFingerprints(book);

  if (fingerprints.length === 0) {
    return (
      <Tooltip title="No fingerprint data">
        <Box
          sx={{
            width: '100%',
            height: 30,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          —
        </Box>
      </Tooltip>
    );
  }

  return (
    <Tooltip title={`${fingerprints.length} frequency bands`}>
      <Box
        sx={{
          width: '100%',
          height: 30,
          display: 'flex',
          gap: 0.5,
          padding: '4px 0',
          backgroundColor: '#f0f0f0',
          borderRadius: '4px',
          overflow: 'hidden',
        }}
      >
        {fingerprints.map((intensity, idx) => {
          // Map intensity to color (blue for low, red for high)
          const hue = 240 - (intensity / 100) * 120; // Blue (240) to Red (0)
          return (
            <Box
              key={idx}
              sx={{
                flex: 1,
                height: '100%',
                backgroundColor: `hsl(${Math.max(0, hue)}, 100%, 50%)`,
              }}
            />
          );
        })}
      </Box>
    </Tooltip>
  );
};
