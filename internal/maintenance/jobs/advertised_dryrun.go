// file: internal/maintenance/jobs/advertised_dryrun.go
// version: 1.0.0
// guid: cb3b8ec2-4f3b-46bd-af8d-aed041a5d393
// last-edited: 2026-11-12

package jobs

import (
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// AdvertisedDryRunDefault reports the dry_run value this job publishes in its
// DefaultParams(), which is what listMaintenanceJobs serves to clients as
// `default_params`. Jobs that expose no dry_run key at all report false.
//
// It goes through JSON deliberately rather than reflecting over the struct.
// DefaultParams() returns `any` — in practice an anonymous struct declared
// inline in each job file — and JSON is the representation the client actually
// received. Reading it the same way the client did is what keeps the advertised
// default and the applied default from drifting apart again; a reflection-based
// reader could disagree with the wire format over tags, embedding or casing,
// which is the exact class of divergence this function exists to close.
//
// Both shells share this one reader: the HTTP dispatcher resolves a request's
// omitted dry_run against it, and the native task kinds declare a job ReadOnly
// (no preview) exactly when it reports false.
func AdvertisedDryRunDefault(job maintenance.MaintenanceJob) bool {
	raw, err := json.Marshal(job.DefaultParams())
	if err != nil {
		return false
	}
	var dp struct {
		DryRun *bool `json:"dry_run"`
	}
	if err := json.Unmarshal(raw, &dp); err != nil || dp.DryRun == nil {
		return false
	}
	return *dp.DryRun
}
