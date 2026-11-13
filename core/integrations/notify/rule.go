// file: core/integrations/notify/rule.go
// version: 1.0.0
// guid: 8c1d5e74-2b69-4f0a-93d7-5a4e0c8b2f13
// last-edited: 2026-11-13

package notify

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/rbac"
)

// Rule is global routing data, written by a holder of core:integrations.manage.
// Rules are small admin data: the Service keeps them in memory, loaded at
// start and replaced when a PutRule or DeleteRule batch commits.
type Rule struct {
	ID          string
	Topics      []string // exact event topic names or "<module>.*"
	MinSeverity Severity
	Instances   []integrations.Ref // global channel instances
	Enabled     bool
}

// Bounds of one rule.
const (
	maxRuleTopics    = 64
	maxRuleInstances = 64
)

// ErrRuleNotFound: DeleteRule or RuleStore.Delete named a rule that is not
// stored.
var ErrRuleNotFound = errors.New("notify: no such rule")

// ErrNoCatalog: a Service or an Importer was built without the events catalog
// it validates a rule's topics against.
var ErrNoCatalog = errors.New("notify: no events catalog to validate topics against")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: notify rule: %s", integrations.ErrInvalid, fmt.Sprintf(format, args...))
}

// validRuleID reports whether id is 1 to 32 characters of [a-z][a-z0-9-]*, the
// grammar of an instance ID.
func validRuleID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z':
		case (c >= '0' && c <= '9') || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// topicPattern reports whether t is "<module>.*" and returns the module.
func topicPattern(t string) (module string, ok bool) {
	module, ok = strings.CutSuffix(t, ".*")
	if !ok || module == "" || strings.ContainsAny(module, ".*") {
		return "", false
	}
	return module, true
}

// matches reports whether the rule pattern covers an event topic name.
func matches(pattern, topic string) bool {
	if module, ok := topicPattern(pattern); ok {
		return strings.HasPrefix(topic, module+".")
	}
	return pattern == topic
}

// validateRule checks r against the events catalog and returns the notifiable
// topics it covers, which PutRule asks the author's permission for. The
// refusals wrap integrations.ErrInvalid (422) and name no instance setting.
func validateRule(r Rule, cat events.Catalog) ([]events.TopicSpec, error) {
	if cat == nil {
		return nil, ErrNoCatalog
	}
	if !validRuleID(r.ID) {
		return nil, invalidf("ID %q must be 1 to 32 characters of [a-z][a-z0-9-]*", r.ID)
	}
	if !r.MinSeverity.Valid() {
		return nil, invalidf("%s: MinSeverity %d is not a severity", r.ID, uint8(r.MinSeverity))
	}
	if len(r.Topics) == 0 || len(r.Topics) > maxRuleTopics {
		return nil, invalidf("%s: needs 1 to %d topics, has %d", r.ID, maxRuleTopics, len(r.Topics))
	}
	if len(r.Instances) == 0 || len(r.Instances) > maxRuleInstances {
		return nil, invalidf("%s: needs 1 to %d instances, has %d", r.ID, maxRuleInstances, len(r.Instances))
	}
	for _, ref := range r.Instances {
		if ref.Kind != integrations.KindNotify {
			return nil, invalidf("%s: instance %q is not a notify instance", r.ID, ref.String())
		}
		if err := ref.Validate(); err != nil {
			return nil, err
		}
	}
	var covered []events.TopicSpec
	for _, pat := range r.Topics {
		if _, isPattern := topicPattern(pat); !isPattern && strings.Contains(pat, "*") {
			return nil, invalidf("%s: topic %q: only a trailing \".*\" after a module is a pattern", r.ID, pat)
		}
		n := 0
		for spec := range cat.All() {
			if spec.Notify == nil || !matches(pat, spec.Name) {
				continue
			}
			n++
			if !slices.ContainsFunc(covered, func(c events.TopicSpec) bool { return c.Name == spec.Name }) {
				covered = append(covered, spec)
			}
		}
		if n > 0 {
			continue
		}
		if _, isPattern := topicPattern(pat); isPattern {
			return nil, fmt.Errorf("%w: notify rule %s: %s matches no notifiable topic", integrations.ErrInvalid, r.ID, pat)
		}
		if _, declared := cat.Lookup(pat); declared {
			return nil, fmt.Errorf("%w: notify rule %s: %s: %w", integrations.ErrInvalid, r.ID, pat, ErrNoNotifyMeta)
		}
		return nil, fmt.Errorf("%w: notify rule %s: %s: %w", integrations.ErrInvalid, r.ID, pat, ErrUndeclaredTopic)
	}
	return covered, nil
}

// authorize reports whether who may route every topic in specs to a shared
// channel: each topic's declared Permission must be satisfied (05-notify §4).
func authorize(who rbac.Checker, ruleID string, specs []events.TopicSpec) error {
	for _, spec := range specs {
		if !who.Satisfies(spec.Permission) {
			return fmt.Errorf("%w: notify rule %s: the caller may not see %s, so may not route it to a shared channel",
				integrations.ErrForbidden, ruleID, spec.Name)
		}
	}
	return nil
}

// covers reports whether r is enabled, names topic and is satisfied by sev.
func (r *Rule) covers(topic string, sev Severity) bool {
	if !r.Enabled || sev < r.MinSeverity {
		return false
	}
	return slices.ContainsFunc(r.Topics, func(p string) bool { return matches(p, topic) })
}

// clone is a deep copy, so a snapshot never shares a slice with a caller.
func (r Rule) clone() Rule {
	r.Topics = slices.Clone(r.Topics)
	r.Instances = slices.Clone(r.Instances)
	return r
}
