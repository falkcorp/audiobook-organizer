// file: internal/books/config_cutover_oracle_test.go
// version: 1.0.0
// guid: d82a4f17-3b69-4e50-a1c8-5f0e93b7d264
// last-edited: 2026-11-20

package books

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// The differential oracle's HEAD side is FROZEN. testdata/config_oracle/{defaults,populated}.json
// were produced at d3d767304, while HEAD's own loaders were still in the tree, by
// saving a Config through HEAD's SaveConfigToDatabase (the "rows": the blob with its
// secrets zeroed, and the secret rows) and loading it back with
// LoadConfigFromDatabase over a copy of the store (the "config": what HEAD's serve
// would have run with). "defaults" saved the shipped defaults. "populated" saved the
// shipped defaults with every scalar moved off its value (a bool flipped, a number
// plus one, a string list lengthened) and the settings a perturbation cannot invent
// set to realistic values: OAuth sign-in on with a GitHub and a Google client id and
// secret, allowed emails, role "editor", a redirect base URL, the OpenAI key, the
// Hardcover token, the Google Books key, and credentials on the google-books and
// hardcover metadata sources. The loaders are deleted (F3-07 loader), so the goldens
// cannot be regenerated; they are read-only evidence of what HEAD produced. What
// reads them now is the schedule and journal-prune parity tests (Config only);
// the differential test that compared them with the M2 cutover went with M2
// (R3-02e-1).

type oracleRow struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	Secret bool   `json:"secret"`
	Value  string `json:"value"`
}

type oracleGolden struct {
	Rows   []oracleRow   `json:"rows"`
	Config config.Config `json:"config"`
}

func loadOracleGolden(t *testing.T, name string) oracleGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "config_oracle", name+".json"))
	require.NoError(t, err)
	var g oracleGolden
	require.NoError(t, json.Unmarshal(raw, &g))
	require.NotEmpty(t, g.Rows)
	return g
}
