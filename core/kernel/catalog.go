// file: core/kernel/catalog.go
// version: 1.0.0
// guid: 0cc15150-0c21-450f-b103-0790a3fd30b8
// last-edited: 2026-11-10

package kernel

import (
	"fmt"

	"github.com/falkcorp/mediacore/rbac"
)

// declareCoreCatalog builds the catalog holding every core:* scope (05-rbac §5
// item 3, IMPL-4). It takes no subsystem and reads no configuration, so the
// scopes are declared whichever optional core subsystems are wired: a seed
// role, a stored grant or a manifest requirement can name any of them.
//
// F2-16 calls it once from the boot sequence, keeps the *rbac.Catalog and
// fails boot on the error. Nothing enforces with the catalog before F4.
func declareCoreCatalog() (*rbac.Catalog, error) {
	c, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		return nil, fmt.Errorf("kernel: declare core scopes: %w", err)
	}
	return c, nil
}
