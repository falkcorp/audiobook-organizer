// file: core/kernel/operator_secrets.go
// version: 1.0.0
// guid: c4d9a0e7-61b2-4f58-8a3d-5e7f12b09c64
// last-edited: 2026-11-19

package kernel

import (
	"encoding/json"
	"fmt"
	"iter"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// operatorSecretSource is *config.Store, ephemeralSink is *secrets.Store: the two
// ends of the hand-off, narrowed so a test can feed it a layer the real store
// would never produce.
type (
	operatorSecretSource interface {
		OperatorSecrets() iter.Seq2[config.Path, config.OperatorValue]
	}
	ephemeralSink interface {
		Ephemeral(n secrets.Name, v secrets.Value, src secrets.Source)
	}
)

// handOperatorSecrets is 05-config §2.4 step 2: every secret key's operator value
// (env, <NAME>_FILE, the config file) is registered with the secret store as an
// ephemeral value, so a reader of the secret store sees it, with Source env or
// file, and every write to that name is refused as locked. config keeps only a
// secret key's reference in its snapshots, so without this hand-off an operator
// secret never reaches Get at all.
//
// A secret supplied by a flag is not here: config refuses one when it loads (it
// would show in the process list). The error below is for a Source this function
// does not know, so a new layer cannot slip a secret past the store silently.
func handOperatorSecrets(st operatorSecretSource, ss ephemeralSink) error {
	for p, v := range st.OperatorSecrets() {
		var text string
		if err := json.Unmarshal(v.Value, &text); err != nil {
			return fmt.Errorf("kernel: config: the operator value of %s (%s) is not a string", p, v.By)
		}
		var src secrets.Source
		switch v.Src {
		case config.SourceEnv:
			src = secrets.SourceEnv
		case config.SourceFile:
			src = secrets.SourceFile
		default:
			return fmt.Errorf("kernel: config: %s supplies the secret %s; set it in the environment (or its _FILE variant) or the config file instead", v.By, p)
		}
		ss.Ephemeral(secrets.Name(p), secrets.NewValue(text), src)
	}
	return nil
}
