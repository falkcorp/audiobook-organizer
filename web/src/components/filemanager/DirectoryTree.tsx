// file: web/src/components/filemanager/DirectoryTree.tsx
// version: 1.1.0
// guid: 6c7d8e9f-0a1b-2c3d-4e5f-6a7b8c9d0e1f

import React, { useState } from 'react';
import {
  Box,
  Typography,
  IconButton,
  Collapse,
  CircularProgress,
} from '@mui/material';
import {
  Folder as FolderIcon,
  FolderOpen as FolderOpenIcon,
  ExpandMore as ExpandMoreIcon,
  ChevronRight as ChevronRightIcon,
  Block as BlockIcon,
} from '@mui/icons-material';

export interface DirectoryNode {
  path: string;
  name: string;
  is_dir: boolean;
  excluded: boolean;
  children?: DirectoryNode[];
}

interface DirectoryTreeProps {
  root: DirectoryNode;
  onSelectDirectory?: (path: string) => void;
  onLoadChildren?: (path: string) => Promise<DirectoryNode[]>;
  selectedPath?: string;
}

export const DirectoryTree: React.FC<DirectoryTreeProps> = ({
  root,
  onSelectDirectory,
  onLoadChildren,
  selectedPath,
}) => {
  return (
    <Box>
      <TreeNode
        node={root}
        level={0}
        onSelectDirectory={onSelectDirectory}
        onLoadChildren={onLoadChildren}
        selectedPath={selectedPath}
      />
    </Box>
  );
};

interface TreeNodeProps {
  node: DirectoryNode;
  level: number;
  onSelectDirectory?: (path: string) => void;
  onLoadChildren?: (path: string) => Promise<DirectoryNode[]>;
  selectedPath?: string;
}

const TreeNode: React.FC<TreeNodeProps> = ({
  node,
  level,
  onSelectDirectory,
  onLoadChildren,
  selectedPath,
}) => {
  const [expanded, setExpanded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [children, setChildren] = useState<DirectoryNode[]>(
    node.children || []
  );

  const handleToggle = async () => {
    if (!node.is_dir) return;

    if (!expanded && children.length === 0 && onLoadChildren) {
      setLoading(true);
      try {
        const loadedChildren = await onLoadChildren(node.path);
        setChildren(loadedChildren);
      } catch (error) {
        console.error('Failed to load children:', error);
      } finally {
        setLoading(false);
      }
    }

    setExpanded(!expanded);
  };

  const handleClick = () => {
    if (node.is_dir) {
      onSelectDirectory?.(node.path);
    }
  };

  const isSelected = selectedPath === node.path;

  return (
    <Box>
      <Box
        sx={[{
          display: 'flex',
          alignItems: 'center',
          pl: level * 2,
          py: 0.5,
          cursor: node.is_dir ? 'pointer' : 'default',
          '&:hover': {
            bgcolor: node.is_dir ? 'action.hover' : 'transparent',
          }
        }, isSelected ? {
          bgcolor: 'action.selected'
        } : {
          bgcolor: 'transparent'
        }]}
        onClick={handleClick}
      >
        {node.is_dir && (
          <IconButton size="small" onClick={handleToggle} sx={{ mr: 0.5 }}>
            {loading ? (
              <CircularProgress size={16} />
            ) : expanded ? (
              <ExpandMoreIcon fontSize="small" />
            ) : (
              <ChevronRightIcon fontSize="small" />
            )}
          </IconButton>
        )}

        {!node.is_dir && <Box sx={{ width: 32 }} />}

        {node.excluded ? (
          <BlockIcon sx={{ mr: 1, color: 'error.main' }} fontSize="small" />
        ) : expanded ? (
          <FolderOpenIcon
            sx={{ mr: 1, color: 'primary.main' }}
            fontSize="small"
          />
        ) : (
          <FolderIcon sx={{ mr: 1, color: 'action.active' }} fontSize="small" />
        )}

        <Typography
          variant="body2"
          sx={{
            color: node.excluded ? 'text.disabled' : 'text.primary',
            textDecoration: node.excluded ? 'line-through' : 'none',
          }}
        >
          {node.name}
        </Typography>
      </Box>

      {node.is_dir && expanded && children.length > 0 && (
        <Collapse in={expanded}>
          {children.map((child) => (
            <TreeNode
              key={child.path}
              node={child}
              level={level + 1}
              onSelectDirectory={onSelectDirectory}
              onLoadChildren={onLoadChildren}
              selectedPath={selectedPath}
            />
          ))}
        </Collapse>
      )}
    </Box>
  );
};
