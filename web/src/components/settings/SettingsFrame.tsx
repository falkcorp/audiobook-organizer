// file: web/src/components/settings/SettingsFrame.tsx
// version: 0.1.0
// guid: aeb5c965-f1b8-471d-95cf-f8fcd3c023c6
// last-edited: 2026-11-21

// The shell half of the Settings page: the heading, the browser-local "Show advanced
// settings" switch, the tab strip (kept in `?tab=<id>`, with the older `#<id>` form
// still read), the tab panels, and slots for notices and a footer bar. It knows
// nothing about what a panel edits, so it imports no books code.

import type { ComponentType, ReactNode } from 'react';
import { useLocation, useSearchParams } from 'react-router';
import { Box, Typography, Paper, Tabs, Tab, Switch, FormControlLabel } from '@mui/material';
import { applyOwnedParams, parseEnum } from '../../hooks/useUrlState';
import { useAdvancedSettings } from '../../hooks/useAdvancedSettings';

export interface SettingsFramePanel {
  /** The tab slug used in the URL. The first panel is the default and is left out of it. */
  id: string;
  label: string;
  Component: ComponentType;
}

export interface SettingsFrameProps {
  panels: readonly SettingsFramePanel[];
  /** Notices shown between the advanced switch and the tabs. */
  banners?: ReactNode;
  /** A bar pinned under the tab panels. */
  footer?: ReactNode;
  title?: string;
}

interface TabPanelProps {
  children?: ReactNode;
  index: number;
  value: number;
}

function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;

  return (
    <Box
      role="tabpanel"
      hidden={value !== index}
      id={`settings-tabpanel-${index}`}
      aria-labelledby={`settings-tab-${index}`}
      sx={{
        overflowY: 'auto',
        overflowX: 'hidden',
        flex: 1,
        minHeight: 0,
        p: 3,
      }}
      {...other}
    >
      {value === index && children}
    </Box>
  );
}

export function SettingsFrame({ panels, banners, footer, title = 'Settings' }: SettingsFrameProps) {
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const ids = panels.map((p) => p.id);
  const defaultId = ids[0];
  // The tab is `?tab=<slug>`, the default left out of the URL. The older `#<slug>` form
  // is still READ when there is no `?tab=` (Audiobookshelf links and old bookmarks use
  // it) and is dropped by the first tab click, which rewrites the location without a hash.
  const tabValue = ids.indexOf(
    parseEnum(searchParams.get('tab') ?? location.hash.replace('#', ''), ids, defaultId)
  );
  const setTabValue = (index: number) =>
    setSearchParams((prev) => applyOwnedParams(prev, { tab: ids[index] }, { tab: defaultId }), {
      replace: true,
    });
  const { showAdvanced, setShowAdvanced } = useAdvancedSettings();

  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        height: '100vh',
        maxHeight: '100vh',
        overflow: 'hidden',
        p: 2,
      }}
    >
      <Typography variant="h4" gutterBottom sx={{ flexShrink: 0 }}>
        {title}
      </Typography>

      {/*
        Global, and deliberately outside the tabs and the Save flow: it only
        changes what this browser shows (menus such as Review → Dedup), takes
        effect immediately, and is stored locally rather than on the server.
      */}
      <Box sx={{ flexShrink: 0, mb: 1 }}>
        <FormControlLabel
          control={
            <Switch
              checked={showAdvanced}
              onChange={(e) => setShowAdvanced(e.target.checked)}
              slotProps={{ input: { 'data-testid': 'show-advanced-settings' } as object }}
            />
          }
          label="Show advanced settings"
        />
        <Typography
          variant="caption"
          color="text.secondary"
          component="div"
          sx={{ ml: 6, mt: -0.5 }}
        >
          Shows expert tools, such as the Advanced sections of the Review page's Dedup and Metadata
          menus. Saved in this browser.
        </Typography>
      </Box>

      {banners}

      <Paper
        sx={{
          display: 'flex',
          flexDirection: 'column',
          flex: 1,
          minHeight: 0,
          overflow: 'hidden',
        }}
      >
        <Box sx={{ borderBottom: 1, borderColor: 'divider' }}>
          <Tabs
            value={tabValue}
            onChange={(_, newValue: number) => setTabValue(newValue)}
            aria-label="settings tabs"
            variant="scrollable"
            scrollButtons="auto"
            allowScrollButtonsMobile
          >
            {panels.map((panel) => (
              <Tab key={panel.id} label={panel.label} />
            ))}
          </Tabs>
        </Box>

        {panels.map(({ id, Component }, index) => (
          <TabPanel key={id} value={tabValue} index={index}>
            <Component />
          </TabPanel>
        ))}

        {footer}
      </Paper>
    </Box>
  );
}
