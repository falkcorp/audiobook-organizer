// file: web/src/modules/books/layout/LibrarySearch.tsx
// version: 0.1.0
// guid: 1e8b4c63-7a95-4d20-9f3e-b5a6c0d2e817
// last-edited: 2026-11-21

import { useEffect, useState } from 'react';
import { useLocation, useNavigate } from 'react-router';
import { Box, IconButton, InputBase, Tooltip } from '@mui/material';
import SearchIcon from '@mui/icons-material/Search';

/**
 * The top bar's library search: a box from the md breakpoint up, an icon
 * button below it. Enter (or the button) goes to /library?search=...; on a
 * /library URL the box follows the URL's `search` param. What TopBar.tsx kept
 * inline before the top bar became a frame with a `center` slot.
 */
export function LibrarySearch() {
  const navigate = useNavigate();
  const location = useLocation();
  const [searchQuery, setSearchQuery] = useState('');

  useEffect(() => {
    if (!location.pathname.startsWith('/library')) {
      return;
    }
    const params = new URLSearchParams(location.search);
    setSearchQuery(params.get('search') || '');
  }, [location.pathname, location.search]);

  const submitSearch = () => {
    const params = new URLSearchParams();
    if (searchQuery.trim()) {
      params.set('search', searchQuery.trim());
    }
    navigate({
      pathname: '/library',
      search: params.toString() ? `?${params.toString()}` : '',
    });
  };

  return (
    <>
      <Box
        sx={{
          display: { xs: 'none', md: 'flex' },
          alignItems: 'center',
          px: 1.5,
          py: 0.5,
          borderRadius: 2,
          bgcolor: 'rgba(255,255,255,0.15)',
          flexGrow: 1,
          maxWidth: 520,
          mr: 2,
        }}
      >
        <SearchIcon fontSize="small" sx={{ mr: 1, opacity: 0.8 }} />
        <InputBase
          value={searchQuery}
          onChange={(event) => setSearchQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              submitSearch();
            }
          }}
          placeholder="Search library..."
          inputProps={{ 'aria-label': 'Search library' }}
          sx={{ color: 'inherit', width: '100%' }}
        />
      </Box>
      <Tooltip title="Search library">
        <IconButton
          color="inherit"
          aria-label="search library"
          onClick={submitSearch}
          sx={{ mr: 1, display: { xs: 'inline-flex', md: 'none' } }}
        >
          <SearchIcon />
        </IconButton>
      </Tooltip>
    </>
  );
}
