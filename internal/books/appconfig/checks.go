// file: internal/books/appconfig/checks.go
// version: 1.0.0
// guid: 0d4f7a3b-5c18-4e92-a6b1-8e3c9f2d7a45
// last-edited: 2026-11-20

package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// Two checks HEAD's update service ran on every PUT that the Engine owns now
// that the service is deleted: a submitted ai_endpoints list is validated, and a
// secret that is only a mask is refused.

// maskCheckedHeads are the secret fields HEAD refused a masked value for
// (checkMaskShapedSecrets). The list is HEAD's, not "every secret": a long
// password that happens to contain a run of asterisks is a real secret for the
// fields HEAD never checked.
var maskCheckedHeads = map[string]bool{
	"openai_api_key":                          true,
	"acoustid_api_key":                        true,
	"google_books_api_key":                    true,
	"hardcover_api_token":                     true,
	"basic_auth_password":                     true,
	"oauth_github_client_secret":              true,
	"oauth_google_client_secret":              true,
	"deluge_web_password":                     true,
	"download_client.torrent.deluge.password": true,
}

// maskShaped is HEAD's database.LooksLikeMask: four or more asterisks in a row.
// HEAD let one through only when it was the exact echo of the stored secret's
// mask, because GET returned that mask. GET returns no secret value now (a
// secret key's Value is nil), so nothing is ever echoed and every masked value is
// a stale client's, never a secret to store.
func maskShaped(s string) bool { return strings.Contains(s, "****") }

func errMaskShaped(field string) error {
	return fmt.Errorf("%s looks like a masked value (it contains \"****\"); this server never returns a stored secret, so re-enter the secret itself", field)
}

// maskedCredentialProblems is the same refusal for a metadata source's
// credential values (HEAD checked metadata_sources[id].credentials.k).
func maskedCredentialProblems(u config.Update) []error {
	raw, ok := u.Set["metadata_sources"]
	if !ok {
		return nil
	}
	var srcs []struct {
		ID          string             `json:"id"`
		Credentials map[string]*string `json:"credentials"`
	}
	if json.Unmarshal(raw, &srcs) != nil {
		return nil // not a list of sources: the field check says so
	}
	var out []error
	for _, s := range srcs {
		keys := make([]string, 0, len(s.Credentials))
		for k := range s.Credentials {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := s.Credentials[k]; v != nil && maskShaped(*v) {
				out = append(out, &coreconfig.FieldError{
					Path: "metadata_sources",
					Err:  errMaskShaped("metadata_sources[" + s.ID + "].credentials." + k),
				})
			}
		}
	}
	return out
}

// endpointsProblems validates the ai_endpoints list a write carries
// (config.ValidateAIEndpoints, HEAD's own check). Only a write that names the key
// is judged, so a row an earlier binary stored with a capability this one does
// not know never blocks an unrelated save.
func endpointsProblems(u config.Update) []error {
	raw, ok := u.Set["ai_endpoints"]
	if !ok {
		return nil
	}
	var rows []config.AIEndpoint
	if json.Unmarshal(raw, &rows) != nil {
		return nil // the field check reports a list that does not decode
	}
	if err := config.ValidateAIEndpoints(rows); err != nil {
		return []error{&coreconfig.FieldError{Path: "ai_endpoints", Err: errors.New(strings.TrimPrefix(err.Error(), "invalid ai_endpoints: "))}}
	}
	return nil
}
