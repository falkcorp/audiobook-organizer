// file: web/src/components/bookdetail/CoverTextPanel.tsx
// version: 1.0.0
// guid: 8c3e1f74-2a9d-4b50-9e67-4d1b8f0a2c93
// last-edited: 2026-09-26
import { useEffect, useState } from 'react';
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Chip,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import TextFieldsIcon from '@mui/icons-material/TextFields';
import { getCoverText, type CoverTextImage } from '../../services/api';

interface Props {
  bookId: string;
}

const SOURCE_LABEL: Record<CoverTextImage['source'], string> = {
  local: 'Cover',
  embedded: 'Embedded cover',
  folder: 'Folder image',
};

function Field({ label, value }: { label: string; value?: string | string[] }) {
  const text = Array.isArray(value) ? value.join('; ') : value;
  if (!text) return null;
  return (
    <Box sx={{ display: 'flex', gap: 1, mb: 0.5 }}>
      <Typography variant="body2" sx={{ color: 'text.secondary', minWidth: 110 }}>
        {label}
      </Typography>
      <Typography variant="body2">{text}</Typography>
    </Box>
  );
}

/**
 * The text a vision model read off the book's cover images. Evidence only:
 * nothing here was applied to the book's metadata. Hidden until at least one
 * cover image has been indexed by maintenance.cover-text-read.
 */
export function CoverTextPanel({ bookId }: Props) {
  const [expanded, setExpanded] = useState(false);
  const [images, setImages] = useState<CoverTextImage[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await getCoverText(bookId);
        if (!cancelled) setImages(res?.images ?? []);
      } catch {
        // The section is optional evidence: a failed fetch hides it.
        if (!cancelled) setImages([]);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [bookId]);

  if (!images || images.length === 0) return null;
  const readCount = images.filter((i) => i.status === 'ok').length;

  return (
    <Accordion
      expanded={expanded}
      onChange={(_, v) => setExpanded(v)}
      disableGutters
      sx={{
        mb: 3,
        '&:before': { display: 'none' },
        borderRadius: 1,
        border: '1px solid',
        borderColor: 'divider',
      }}
      elevation={0}
    >
      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <TextFieldsIcon fontSize="small" color="action" />
          <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
            Cover text
          </Typography>
          <Chip
            label={`${readCount}/${images.length} read`}
            size="small"
            variant="outlined"
            color={readCount === images.length ? 'success' : 'default'}
          />
        </Box>
      </AccordionSummary>
      <AccordionDetails>
        {images.map((img) => (
          <Box key={img.hash} sx={{ mb: 2 }} data-testid="cover-text-image">
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              {SOURCE_LABEL[img.source] ?? img.source}
              {img.model && (
                <Typography
                  component="span"
                  variant="caption"
                  sx={{ color: 'text.secondary', ml: 1 }}
                >
                  {img.model}
                  {img.read_at ? ` · ${new Date(img.read_at).toLocaleString()}` : ''}
                </Typography>
              )}
            </Typography>
            {img.status === 'ok' && img.text && (
              <>
                <Field label="Title" value={img.text.title} />
                <Field label="Subtitle" value={img.text.subtitle} />
                <Field label="Author" value={img.text.authors} />
                <Field label="Narrator" value={img.text.narrators} />
                <Field
                  label="Series"
                  value={
                    img.text.series
                      ? `${img.text.series}${img.text.series_number ? ` #${img.text.series_number}` : ''}`
                      : undefined
                  }
                />
                <Field label="Publisher" value={img.text.publisher} />
                <Field label="Other text" value={img.text.other_text} />
              </>
            )}
            {img.status === 'ok' && (!img.text || Object.keys(img.text).length === 0) && (
              <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                No legible text.
              </Typography>
            )}
            {img.status === 'error' && (
              <Typography variant="body2" color="error">
                Read failed: {img.error}
              </Typography>
            )}
            {!img.status && (
              <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                Not read yet.
              </Typography>
            )}
          </Box>
        ))}
      </AccordionDetails>
    </Accordion>
  );
}
