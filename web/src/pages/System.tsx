// file: web/src/pages/System.tsx
// version: 1.6.1
// guid: 7c8d9e0f-1a2b-3c4d-5e6f-7a8b9c0d1e2f
// last-edited: 2026-11-07

import { useNavigate } from 'react-router';
import { Box, Typography, Paper, Tabs, Tab, Button } from '@mui/material';
import { OpenInNew as OpenInNewIcon } from '@mui/icons-material';
import { StorageTab } from '../components/system/StorageTab';
import { SystemInfoTab } from '../components/system/SystemInfoTab';
import { QuotaTab } from '../components/system/QuotaTab';
import { MaintenanceTab } from '../components/system/MaintenanceTab';
import { useUrlEnumParam } from '../hooks/useUrlState';

// The tab is `?tab=<slug>`; System Info is the default and is left out of the URL.
const SYSTEM_TABS = ['info', 'storage', 'quota', 'maintenance'] as const;
type SystemTab = (typeof SYSTEM_TABS)[number];

interface TabPanelProps {
  children?: React.ReactNode;
  index: SystemTab;
  value: SystemTab;
}

function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;

  return (
    <div
      role="tabpanel"
      hidden={value !== index}
      id={`system-tabpanel-${index}`}
      aria-labelledby={`system-tab-${index}`}
      {...other}
    >
      {value === index && <Box sx={{ py: 3 }}>{children}</Box>}
    </div>
  );
}

export function System() {
  const [tabValue, setTabValue] = useUrlEnumParam('tab', SYSTEM_TABS, 'info');
  const navigate = useNavigate();

  return (
    <Box>
      <Typography variant="h4" gutterBottom>
        System
      </Typography>

      <Paper>
        <Tabs
          value={tabValue}
          onChange={(_, newValue) => setTabValue(newValue)}
          aria-label="system tabs"
          variant="scrollable"
          scrollButtons="auto"
          allowScrollButtonsMobile
        >
          <Tab label="System Info" value="info" />
          <Tab label="Storage" value="storage" />
          <Tab label="Quota" value="quota" />
          <Tab label="Maintenance" value="maintenance" />
        </Tabs>

        <TabPanel value={tabValue} index="info">
          <SystemInfoTab />
          <Box sx={{ mt: 2, px: 3, pb: 2 }}>
            <Button
              variant="outlined"
              startIcon={<OpenInNewIcon />}
              onClick={() => navigate('/activity')}
            >
              View Activity Log
            </Button>
          </Box>
        </TabPanel>

        <TabPanel value={tabValue} index="storage">
          <StorageTab />
        </TabPanel>

        <TabPanel value={tabValue} index="quota">
          <QuotaTab />
        </TabPanel>

        <TabPanel value={tabValue} index="maintenance">
          <MaintenanceTab />
        </TabPanel>
      </Paper>
    </Box>
  );
}
