// file: core/module/validation.go
// version: 1.0.0
// guid: bd6fcb40-45ea-4d20-b0a1-cb2fe9ec8105
// last-edited: 2026-11-07

package module

import (
	"errors"
	"fmt"
	"strings"
)

// ValidationError lists every problem found in one boot, across modules
// (06 §6.4.7). P1 and P6 return one; P7 returns one too, with each httpapi
// registration error carried as a Problem whose cause errors.Is still finds.
type ValidationError struct{ Problems []Problem }

// Error is one line per problem.
func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.Error()
	}
	return strings.Join(lines, "\n")
}

// Unwrap returns each Problem as an error, for errors.Is.
func (e *ValidationError) Unwrap() []error {
	out := make([]error, len(e.Problems))
	for i, p := range e.Problems {
		out[i] = p
	}
	return out
}

// Problem is one validation problem.
type Problem struct {
	Module ID // "" for app-level problems
	Kind   ProblemKind
	Detail string // what, where, and how to fix

	cause error // the error the problem was made from (an httpapi registration error), or nil
}

// Error is "module <id>: <kind>: <detail>", or "<kind>: <detail>" for an
// app-level problem.
func (p Problem) Error() string {
	s := p.Kind.String() + ": " + p.Detail
	if p.Module != "" {
		s = "module " + string(p.Module) + ": " + s
	}
	return s
}

// Is matches the ProblemKind sentinel, so errors.Is(err, module.ErrCoreTooOld)
// finds a ProblemCoreTooOld anywhere in a joined error.
func (p Problem) Is(target error) bool {
	s := p.Kind.sentinel()
	return s != nil && target == s
}

// Unwrap returns the error the problem was made from, so errors.Is also finds
// an httpapi sentinel (httpapi.ErrBadBinding, ...) through a P7 problem.
func (p Problem) Unwrap() error { return p.cause }

// NewProblem is a Problem with a cause. Kernel-only: the kernel turns the
// errors httpapi's Router.Build returns into problems (P7) without losing
// them for errors.Is.
func NewProblem(module ID, kind ProblemKind, cause error) Problem {
	return Problem{Module: module, Kind: kind, Detail: cause.Error(), cause: cause}
}

// ProblemKind names the rule a Problem breaks.
type ProblemKind int

const (
	ProblemInvalidID ProblemKind = iota + 1
	ProblemReservedID
	ProblemDuplicateID
	ProblemCoreTooOld
	ProblemPermissionNamespace
	ProblemDuplicatePermission
	ProblemUndeclaredPermission
	ProblemKeyspaceOverlap
	ProblemMigrationOrder
	ProblemSettingsNamespace
	ProblemSettingsUndeclared
	ProblemTaskKindNamespace
	ProblemDuplicateTaskKind
	ProblemTopicNamespace
	ProblemTopicUndeclared          // outbox handler or raw subscription names a topic no module declares (or not Durable)
	ProblemTaskLedger               // a registered kind is missing from Manifest.TaskLedger
	ProblemOutboxConsumerUndeclared // consumer name not in Manifest.OutboxConsumers
	ProblemRouteOutsidePrefix
	ProblemRouteWithoutPermission
	ProblemLegacyPrefixCollision
	ProblemSPAOwner // two ServesSPA mounts, or a ServesSPA mount with a non-zero app.Options.UI
	ProblemUIDescriptor
	ProblemCapabilityMissing
	ProblemUnknownModuleInConfig
	ProblemRegistrarClosed
	ProblemWorkerPolicy
	ProblemAdapter
	ProblemChangeSinkDuplicate
	ProblemTaskStartUnknown    // Manifest.TaskStarts names a kind that is neither registered nor in any manifest's TaskLedger (SEC2-15, COR3-10)
	ProblemAuthMethod          // AuthMethod spec interactive, credential-changing, owner-verifying, or not "<ID>.<name>" (SEC2-16)
	ProblemLegacyAuth          // LegacyAuthGin by a module other than Options.LegacyAuthGinUntil, or OwnAuth without a registered module method (SEC2-17)
	ProblemDerivedIndex        // derived index declared without hooks, or hooks for an undeclared index (COR2-15)
	ProblemSPASegmentCollision // a ui.ReservedSegments() entry is also a NonSPA segment (P8, FE2-7); the ONE name for this check (FE3-9)
	ProblemImportedRole        // a Manifest.ImportedRoleIDs entry listed by two manifests or equal to a core seed role ID (P1, COR5-5)
	ProblemResetHook           // a second manifest declares a ResetHook (P1; 05-storage §5S.3.15 allows one per App)
	ProblemInvalidRegistration // coined in R1-13, appended: a registration that breaks a rule no kind above names (a hook name not "<ID>.<name>" or used twice, a nil worker Run, a second LogOtherUserData, a route httpapi refuses for its shape: binding, schema, JSON name, duplicate or conflicting pattern, Public outside PublicRoutes, no auth service)

	// ProblemUIAppIDMismatch is appended with ui (06 §6.4.7), after
	// ProblemInvalidRegistration, so adding it renumbers no built kind; the
	// deferred-build facade kinds follow it when arrfacade lands (SIMP3-11).
)

// Sentinels for errors.Is(err, module.ErrCoreTooOld) etc.: one per ProblemKind.
var (
	ErrInvalidID                = errors.New("module: invalid ID")
	ErrReservedID               = errors.New("module: reserved ID")
	ErrDuplicateID              = errors.New("module: duplicate ID")
	ErrCoreTooOld               = errors.New("module: core too old")
	ErrPermissionNamespace      = errors.New("module: permission namespace")
	ErrDuplicatePermission      = errors.New("module: duplicate permission")
	ErrUndeclaredPermission     = errors.New("module: undeclared permission")
	ErrKeyspaceOverlap          = errors.New("module: keyspace overlap")
	ErrMigrationOrder           = errors.New("module: migration order")
	ErrSettingsNamespace        = errors.New("module: settings namespace")
	ErrSettingsUndeclared       = errors.New("module: settings undeclared")
	ErrTaskKindNamespace        = errors.New("module: task kind namespace")
	ErrDuplicateTaskKind        = errors.New("module: duplicate task kind")
	ErrTopicNamespace           = errors.New("module: topic namespace")
	ErrTopicUndeclared          = errors.New("module: topic undeclared")
	ErrTaskLedger               = errors.New("module: task ledger")
	ErrOutboxConsumerUndeclared = errors.New("module: outbox consumer undeclared")
	ErrRouteOutsidePrefix       = errors.New("module: route outside prefix")
	ErrRouteWithoutPermission   = errors.New("module: route without permission")
	ErrLegacyPrefixCollision    = errors.New("module: legacy prefix collision")
	ErrSPAOwner                 = errors.New("module: SPA owner")
	ErrUIDescriptor             = errors.New("module: UI descriptor")
	ErrCapabilityMissing        = errors.New("module: capability missing")
	ErrUnknownModuleInConfig    = errors.New("module: unknown module in config")
	ErrRegistrarClosed          = errors.New("module: registrar closed")
	ErrWorkerPolicy             = errors.New("module: worker policy")
	ErrAdapter                  = errors.New("module: adapter")
	ErrChangeSinkDuplicate      = errors.New("module: change sink duplicate")
	ErrTaskStartUnknown         = errors.New("module: task start unknown")
	ErrAuthMethod               = errors.New("module: auth method")
	ErrLegacyAuth               = errors.New("module: legacy auth")
	ErrDerivedIndex             = errors.New("module: derived index")
	ErrSPASegmentCollision      = errors.New("module: SPA segment collision")
	ErrImportedRole             = errors.New("module: imported role")
	ErrResetHook                = errors.New("module: reset hook")
	ErrInvalidRegistration      = errors.New("module: invalid registration")
)

// String is the kind's name without the Problem prefix, e.g. "CoreTooOld".
func (k ProblemKind) String() string {
	switch k {
	case ProblemInvalidID:
		return "InvalidID"
	case ProblemReservedID:
		return "ReservedID"
	case ProblemDuplicateID:
		return "DuplicateID"
	case ProblemCoreTooOld:
		return "CoreTooOld"
	case ProblemPermissionNamespace:
		return "PermissionNamespace"
	case ProblemDuplicatePermission:
		return "DuplicatePermission"
	case ProblemUndeclaredPermission:
		return "UndeclaredPermission"
	case ProblemKeyspaceOverlap:
		return "KeyspaceOverlap"
	case ProblemMigrationOrder:
		return "MigrationOrder"
	case ProblemSettingsNamespace:
		return "SettingsNamespace"
	case ProblemSettingsUndeclared:
		return "SettingsUndeclared"
	case ProblemTaskKindNamespace:
		return "TaskKindNamespace"
	case ProblemDuplicateTaskKind:
		return "DuplicateTaskKind"
	case ProblemTopicNamespace:
		return "TopicNamespace"
	case ProblemTopicUndeclared:
		return "TopicUndeclared"
	case ProblemTaskLedger:
		return "TaskLedger"
	case ProblemOutboxConsumerUndeclared:
		return "OutboxConsumerUndeclared"
	case ProblemRouteOutsidePrefix:
		return "RouteOutsidePrefix"
	case ProblemRouteWithoutPermission:
		return "RouteWithoutPermission"
	case ProblemLegacyPrefixCollision:
		return "LegacyPrefixCollision"
	case ProblemSPAOwner:
		return "SPAOwner"
	case ProblemUIDescriptor:
		return "UIDescriptor"
	case ProblemCapabilityMissing:
		return "CapabilityMissing"
	case ProblemUnknownModuleInConfig:
		return "UnknownModuleInConfig"
	case ProblemRegistrarClosed:
		return "RegistrarClosed"
	case ProblemWorkerPolicy:
		return "WorkerPolicy"
	case ProblemAdapter:
		return "Adapter"
	case ProblemChangeSinkDuplicate:
		return "ChangeSinkDuplicate"
	case ProblemTaskStartUnknown:
		return "TaskStartUnknown"
	case ProblemAuthMethod:
		return "AuthMethod"
	case ProblemLegacyAuth:
		return "LegacyAuth"
	case ProblemDerivedIndex:
		return "DerivedIndex"
	case ProblemSPASegmentCollision:
		return "SPASegmentCollision"
	case ProblemImportedRole:
		return "ImportedRole"
	case ProblemResetHook:
		return "ResetHook"
	case ProblemInvalidRegistration:
		return "InvalidRegistration"
	default:
		return fmt.Sprintf("ProblemKind(%d)", int(k))
	}
}

// sentinel is k's Err value, or nil for an unknown kind.
func (k ProblemKind) sentinel() error {
	switch k {
	case ProblemInvalidID:
		return ErrInvalidID
	case ProblemReservedID:
		return ErrReservedID
	case ProblemDuplicateID:
		return ErrDuplicateID
	case ProblemCoreTooOld:
		return ErrCoreTooOld
	case ProblemPermissionNamespace:
		return ErrPermissionNamespace
	case ProblemDuplicatePermission:
		return ErrDuplicatePermission
	case ProblemUndeclaredPermission:
		return ErrUndeclaredPermission
	case ProblemKeyspaceOverlap:
		return ErrKeyspaceOverlap
	case ProblemMigrationOrder:
		return ErrMigrationOrder
	case ProblemSettingsNamespace:
		return ErrSettingsNamespace
	case ProblemSettingsUndeclared:
		return ErrSettingsUndeclared
	case ProblemTaskKindNamespace:
		return ErrTaskKindNamespace
	case ProblemDuplicateTaskKind:
		return ErrDuplicateTaskKind
	case ProblemTopicNamespace:
		return ErrTopicNamespace
	case ProblemTopicUndeclared:
		return ErrTopicUndeclared
	case ProblemTaskLedger:
		return ErrTaskLedger
	case ProblemOutboxConsumerUndeclared:
		return ErrOutboxConsumerUndeclared
	case ProblemRouteOutsidePrefix:
		return ErrRouteOutsidePrefix
	case ProblemRouteWithoutPermission:
		return ErrRouteWithoutPermission
	case ProblemLegacyPrefixCollision:
		return ErrLegacyPrefixCollision
	case ProblemSPAOwner:
		return ErrSPAOwner
	case ProblemUIDescriptor:
		return ErrUIDescriptor
	case ProblemCapabilityMissing:
		return ErrCapabilityMissing
	case ProblemUnknownModuleInConfig:
		return ErrUnknownModuleInConfig
	case ProblemRegistrarClosed:
		return ErrRegistrarClosed
	case ProblemWorkerPolicy:
		return ErrWorkerPolicy
	case ProblemAdapter:
		return ErrAdapter
	case ProblemChangeSinkDuplicate:
		return ErrChangeSinkDuplicate
	case ProblemTaskStartUnknown:
		return ErrTaskStartUnknown
	case ProblemAuthMethod:
		return ErrAuthMethod
	case ProblemLegacyAuth:
		return ErrLegacyAuth
	case ProblemDerivedIndex:
		return ErrDerivedIndex
	case ProblemSPASegmentCollision:
		return ErrSPASegmentCollision
	case ProblemImportedRole:
		return ErrImportedRole
	case ProblemResetHook:
		return ErrResetHook
	case ProblemInvalidRegistration:
		return ErrInvalidRegistration
	default:
		return nil
	}
}
