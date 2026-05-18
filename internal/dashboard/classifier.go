package dashboard

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

// defaultClaudeRule matches `claude-(opus|sonnet|haiku)-MAJOR-MINOR` with any
// suffix (date snapshot, `[1m]`, etc.) and lets the classifier collapse
// versioned snapshots into a single `family-MAJOR.MINOR` bucket. It runs
// only when no user-defined rule matches first.
var defaultClaudeRule = regexp.MustCompile(`(?i)^claude-(opus|sonnet|haiku)-(\d+)-(\d+)`)

// Classifier maps a raw OTLP model attribute (e.g. `claude-opus-4-7[1m]`,
// `deepseek-v3`) to a stable group key used for dashboard aggregation.
//
// Lookup order:
//  1. User-defined rules from config.DashboardConfig.ModelGroups (in order)
//  2. Built-in Claude rule → "<family>-<major>.<minor>"
//  3. Pass-through: raw model string
type Classifier struct {
	userRules []compiledRule
}

type compiledRule struct {
	re    *regexp.Regexp
	group string
}

// NewClassifier compiles the user rules. config.Load already validates the
// patterns once, so reaching the error path here implies a programming bug.
func NewClassifier(rules []config.ModelGroupRule) (*Classifier, error) {
	c := &Classifier{userRules: make([]compiledRule, 0, len(rules))}
	for i, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("model_groups[%d] pattern %q: %w", i, r.Pattern, err)
		}
		c.userRules = append(c.userRules, compiledRule{re: re, group: r.Group})
	}
	return c, nil
}

// Classify returns the aggregation group for one raw model string.
//
// Empty input maps to "" — callers should drop such rows (the SQL queries
// already filter `model IS NOT NULL`).
func (c *Classifier) Classify(model string) string {
	if model == "" {
		return ""
	}
	for _, r := range c.userRules {
		if idx := r.re.FindStringSubmatchIndex(model); idx != nil {
			return string(r.re.ExpandString(nil, r.group, model, idx))
		}
	}
	if m := defaultClaudeRule.FindStringSubmatch(model); m != nil {
		return fmt.Sprintf("%s-%s.%s", strings.ToLower(m[1]), m[2], m[3])
	}
	return model
}
