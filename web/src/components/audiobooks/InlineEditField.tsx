// file: web/src/components/audiobooks/InlineEditField.tsx
// version: 1.0.1
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b

import React, { useState } from 'react';
import { TextField, IconButton, Box } from '@mui/material';
import { Check as CheckIcon, Close as CloseIcon, Edit as EditIcon } from '@mui/icons-material';

interface InlineEditFieldProps {
  value: string;
  onSave: (newValue: string) => void;
  label?: string;
  multiline?: boolean;
  disabled?: boolean;
}

export const InlineEditField: React.FC<InlineEditFieldProps> = ({
  value,
  onSave,
  label,
  multiline = false,
  disabled = false,
}) => {
  const [isEditing, setIsEditing] = useState(false);
  const [editValue, setEditValue] = useState(value);

  const handleEdit = () => {
    setEditValue(value);
    setIsEditing(true);
  };

  const handleSave = () => {
    onSave(editValue);
    setIsEditing(false);
  };

  const handleCancel = () => {
    setEditValue(value);
    setIsEditing(false);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !multiline) {
      e.preventDefault();
      handleSave();
    } else if (e.key === 'Escape') {
      handleCancel();
    }
  };

  if (isEditing) {
    return (
      <Box display="flex" alignItems="center" gap={1}>
        <TextField
          fullWidth
          value={editValue}
          onChange={(e) => setEditValue(e.target.value)}
          onKeyDown={handleKeyDown}
          label={label}
          multiline={multiline}
          rows={multiline ? 3 : 1}
          autoFocus
          size="small"
        />
        <IconButton size="small" color="primary" onClick={handleSave}>
          <CheckIcon />
        </IconButton>
        <IconButton size="small" onClick={handleCancel}>
          <CloseIcon />
        </IconButton>
      </Box>
    );
  }

  return (
    <Box display="flex" alignItems="center" gap={1}>
      <Box flexGrow={1}>{value || <em style={{ color: '#999' }}>Not set</em>}</Box>
      {!disabled && (
        <IconButton size="small" onClick={handleEdit}>
          <EditIcon fontSize="small" />
        </IconButton>
      )}
    </Box>
  );
};
