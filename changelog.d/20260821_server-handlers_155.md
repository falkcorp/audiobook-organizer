### Changed

#### Task-scheduler and maintenance-window endpoints moved off the legacy v1 operations handler

`GET /tasks`, `POST /tasks/:name/run`, `PUT /tasks/:name`, `POST
/maintenance-window/run`, `GET /maintenance-window/status`, and `PUT
/maintenance-window/config` now live on their own `SchedulerHandler`
(`internal/server/handlers/scheduler_admin.go`) instead of
`internal/server/handlers/operations.Handler`. These six routes are scheduler
configuration and control, not v1 operation records, and were never coupled
to the legacy `operations` table — bundling them with the v1 handler meant
retiring that legacy surface elsewhere in this backlog would have read as
"delete task scheduling." Route paths, request/response shapes, and
permissions (`auth.PermSettingsManage` throughout) are unchanged; this is
purely an internal code-organization move with no behavior change.
