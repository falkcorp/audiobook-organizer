// file: web/src/modules/books/pages/SystemSettingsPanel.tsx
// version: 0.1.0
// guid: 5a5d9689-344c-4575-80bc-3a4b86254644
// last-edited: 2026-11-21

// The System Info tab: system info, auto-update, maintenance and the danger zone.

import { Typography, Paper } from '@mui/material';
import { SystemInfoTab } from '../../../components/system/SystemInfoTab';
import { MaintenanceSettingsSection } from '../../../components/settings/MaintenanceSettingsSection';
import { AutoUpdateSection } from '../../../components/settings/AutoUpdateSection';
import { useBooksSettings } from './booksSettingsContext';

export function SystemSettingsPanel() {
  const { handleMaintenanceChange, maintenanceConfig, setSettings, settings } = useBooksSettings();

  return (
    <>
      <SystemInfoTab />

      <AutoUpdateSection settings={settings} setSettings={setSettings} />

      <MaintenanceSettingsSection config={maintenanceConfig} onChange={handleMaintenanceChange} />

      <Paper
        sx={{
          mt: 4,
          p: 3,
          border: 2,
          borderColor: 'error.main',
          borderRadius: 1,
        }}
      >
        <Typography variant="h6" color="error" gutterBottom>
          Danger Zone
        </Typography>
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
          }}
        >
          Factory reset now runs from the command line with the service stopped. Stop the service,
          then run <code>audiobook-organizer storage reset</code>.
        </Typography>
      </Paper>
    </>
  );
}
