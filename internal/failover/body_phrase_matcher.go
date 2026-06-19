package failover

import (
	"fmt"
	"strings"
)

// BodyPhraseRule matches a response body phrase for a status-code scope.
type BodyPhraseRule struct {
	statusCodes StatusCodeMatcher
	anyStatus   bool
	phrase      string
}

// BodyPhraseMatcher matches upstream response bodies against configured phrases.
// The zero value matches nothing.
type BodyPhraseMatcher struct {
	rules []BodyPhraseRule
}

// ParseBodyPhraseMatcher parses body phrase rules.
//
// Spec grammar:
//   - One rule per line or separated by semicolon.
//   - Rule format: "<status-code-or-range>:<phrase>"
//   - "*" can be used instead of a status code to match any status.
//
// Examples:
//   - "400:insufficient quota"
//   - "400-499:rate limit; 503:overloaded"
//   - "*:temporarily unavailable"
func ParseBodyPhraseMatcher(spec string) (BodyPhraseMatcher, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return BodyPhraseMatcher{}, nil
	}

	rawRules := splitBodyPhraseRules(spec)
	rules := make([]BodyPhraseRule, 0, len(rawRules))

	for _, rawRule := range rawRules {
		ruleSpec := strings.TrimSpace(rawRule)
		if ruleSpec == "" {
			continue
		}

		statusSpec, phrase, ok := strings.Cut(ruleSpec, ":")
		if !ok {
			return BodyPhraseMatcher{}, fmt.Errorf("invalid body phrase rule %q: expected status:phrase", ruleSpec)
		}

		statusSpec = strings.TrimSpace(statusSpec)
		phrase = strings.TrimSpace(phrase)
		if statusSpec == "" {
			return BodyPhraseMatcher{}, fmt.Errorf("invalid body phrase rule %q: status is required", ruleSpec)
		}
		if phrase == "" {
			return BodyPhraseMatcher{}, fmt.Errorf("invalid body phrase rule %q: phrase is required", ruleSpec)
		}

		if statusSpec == "*" {
			rules = append(rules, BodyPhraseRule{
				anyStatus: true,
				phrase:    phrase,
			})
			continue
		}

		statusCodes, err := ParseStatusCodeMatcher(statusSpec)
		if err != nil {
			return BodyPhraseMatcher{}, fmt.Errorf("invalid status in body phrase rule %q: %w", ruleSpec, err)
		}
		if statusCodes.IsEmpty() {
			return BodyPhraseMatcher{}, fmt.Errorf("invalid body phrase rule %q: status is required", ruleSpec)
		}

		rules = append(rules, BodyPhraseRule{
			statusCodes: statusCodes,
			phrase:      phrase,
		})
	}

	if len(rules) == 0 {
		return BodyPhraseMatcher{}, nil
	}

	return BodyPhraseMatcher{rules: rules}, nil
}

// IsEmpty reports whether the matcher has no rules configured.
func (m BodyPhraseMatcher) IsEmpty() bool {
	return len(m.rules) == 0
}

// MightMatchStatus reports whether any configured rule applies to the status code.
func (m BodyPhraseMatcher) MightMatchStatus(statusCode int) bool {
	for _, rule := range m.rules {
		if rule.anyStatus || rule.statusCodes.Match(statusCode) {
			return true
		}
	}
	return false
}

// HasStatusSpecificRule reports whether a non-wildcard rule applies to the status code.
func (m BodyPhraseMatcher) HasStatusSpecificRule(statusCode int) bool {
	for _, rule := range m.rules {
		if !rule.anyStatus && rule.statusCodes.Match(statusCode) {
			return true
		}
	}
	return false
}

// Match returns the matched phrase when body contains a phrase configured for statusCode.
func (m BodyPhraseMatcher) Match(statusCode int, body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}

	bodyText := string(body)
	for _, rule := range m.rules {
		if !rule.anyStatus && !rule.statusCodes.Match(statusCode) {
			continue
		}
		if strings.Contains(bodyText, rule.phrase) {
			return rule.phrase, true
		}
	}

	return "", false
}

func splitBodyPhraseRules(spec string) []string {
	return strings.FieldsFunc(spec, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ';'
	})
}
