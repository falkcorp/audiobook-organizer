// file: web/src/components/CoverLightbox.tsx
// version: 1.1.0
// guid: 7f8e9a0b-1c2d-3e4f-5a6b-7c8d9e0f1a2b
// last-edited: 2026-09-13
//
// Full-size cover viewer. Closes on Escape, on a backdrop click, on the close
// button, and on a click on the image itself.
//
// SIZING
//
// The image is bounded in VIEWPORT units (90vw x 90vh, object-fit: contain) and
// otherwise shows at its natural resolution. It must not be sized as a
// percentage of its container: the container here is a shrink-to-fit box, so a
// `max-width: 100%` image has nothing definite to be a percentage OF, and any
// state in which the image has no intrinsic size (not loaded yet, failed to
// load) collapses the whole viewer to a few pixels.
//
// FAILURE
//
// A cover that fails to load renders a visible message instead of a collapsed
// broken-image box, so "the cover URL is dead" is distinguishable from "the
// viewer is broken".

import { useState } from 'react';
import { Modal, Box, IconButton, Typography } from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import MusicNoteIcon from '@mui/icons-material/MusicNote';

export interface CoverLightboxProps {
  open: boolean;
  src: string | null;
  onClose: () => void;
}

export function CoverLightbox({ open, src, onClose }: CoverLightboxProps) {
  // Keyed on src so a new cover gets a fresh load state.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const failed = src !== null && failedSrc === src;

  return (
    <Modal
      open={open}
      onClose={onClose}
      sx={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        backdropFilter: 'blur(4px)',
      }}
    >
      <Box
        role="dialog"
        aria-label="Cover preview"
        data-testid="cover-lightbox"
        sx={{ position: 'relative', outline: 'none' }}
      >
        <IconButton
          aria-label="Close"
          onClick={onClose}
          sx={{
            position: 'absolute',
            right: 8,
            top: 8,
            bgcolor: 'background.paper',
            '&:hover': { bgcolor: 'action.hover' },
            zIndex: 1,
          }}
        >
          <CloseIcon />
        </IconButton>

        {src && !failed ? (
          <img
            src={src}
            alt="Cover preview"
            data-testid="cover-lightbox-img"
            onClick={onClose}
            onError={() => setFailedSrc(src)}
            style={{
              display: 'block',
              width: 'auto',
              height: 'auto',
              maxWidth: '90vw',
              maxHeight: '90vh',
              objectFit: 'contain',
              cursor: 'zoom-out',
              borderRadius: 4,
            }}
          />
        ) : (
          <Box
            data-testid="cover-placeholder"
            sx={{
              width: 'min(400px, 90vw)',
              height: 'min(500px, 90vh)',
              display: 'flex',
              flexDirection: 'column',
              gap: 2,
              alignItems: 'center',
              justifyContent: 'center',
              bgcolor: 'background.paper',
              borderRadius: 1,
              p: 2,
              textAlign: 'center',
            }}
          >
            <MusicNoteIcon sx={{ fontSize: 80, opacity: 0.3 }} />
            {failed && (
              <Typography variant="body2" color="error" sx={{ wordBreak: 'break-all' }}>
                The cover image failed to load: {src}
              </Typography>
            )}
          </Box>
        )}
      </Box>
    </Modal>
  );
}
