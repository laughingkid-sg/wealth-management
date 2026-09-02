// Package parserrules evaluates versioned, global deterministic source parser
// rules. Rules are deliberately RE2-only and malformed rules are ignored so a
// configuration mistake cannot block model parsing for a user's source.
package parserrules

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

type Rule struct {
	ID               string
	Version          int
	Priority         int
	SenderMatcher    string
	ContentMatcher   string
	ExtractionConfig json.RawMessage
}

// ExtractionConfig has typed field names. constants are deterministic values;
// extractors use an RE2 pattern and a capture group. The supported fields are
// transaction_kind, original_amount_minor, original_currency, sgd_amount_minor,
// occurred_at, merchant_name, title, references, card_last_four,
// masked_bank_reference, additional_identifiers, and category_leaf_name.
type ExtractionConfig struct {
	Constants  map[string]string       `json:"constants"`
	Extractors map[string]CaptureField `json:"extractors"`
}

type CaptureField struct {
	Pattern string `json:"pattern"`
	Group   int    `json:"group"`
}

type AppliedRule struct {
	ID      string
	Version int
	Values  map[string][]string
}

func MatchAndApply(sender, content string, rules []Rule) (AppliedRule, bool) {
	sorted := append([]Rule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority == sorted[j].Priority {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Priority > sorted[j].Priority
	})
	for _, rule := range sorted {
		applied, ok := applyOne(sender, content, rule)
		if ok {
			return applied, true
		}
	}
	return AppliedRule{}, false
}

func applyOne(sender, content string, rule Rule) (AppliedRule, bool) {
	if strings.TrimSpace(rule.ID) == "" || rule.Version < 1 {
		return AppliedRule{}, false
	}
	if !matches(rule.SenderMatcher, sender) || !matches(rule.ContentMatcher, content) {
		return AppliedRule{}, false
	}
	var config ExtractionConfig
	if len(rule.ExtractionConfig) == 0 || json.Unmarshal(rule.ExtractionConfig, &config) != nil {
		return AppliedRule{}, false
	}
	values := make(map[string][]string)
	for field, value := range config.Constants {
		if !supportedField(field) || strings.TrimSpace(value) == "" {
			return AppliedRule{}, false
		}
		values[field] = []string{strings.TrimSpace(value)}
	}
	for field, extractor := range config.Extractors {
		if !supportedField(field) || strings.TrimSpace(extractor.Pattern) == "" || extractor.Group < 0 {
			return AppliedRule{}, false
		}
		re, err := regexp.Compile(extractor.Pattern)
		if err != nil {
			return AppliedRule{}, false
		}
		matches := re.FindAllStringSubmatch(content, 10)
		captured := make([]string, 0, len(matches))
		for _, match := range matches {
			if extractor.Group >= len(match) || strings.TrimSpace(match[extractor.Group]) == "" {
				continue
			}
			captured = append(captured, strings.TrimSpace(match[extractor.Group]))
		}
		if len(captured) > 0 {
			values[field] = captured
		}
	}
	return AppliedRule{ID: rule.ID, Version: rule.Version, Values: values}, true
}

func matches(pattern, value string) bool {
	if strings.TrimSpace(pattern) == "" {
		return true
	}
	re, err := regexp.Compile(pattern)
	return err == nil && re.MatchString(value)
}

func supportedField(field string) bool {
	switch field {
	case "transaction_kind", "original_amount_minor", "original_currency", "sgd_amount_minor", "occurred_at", "merchant_name", "title", "references", "card_last_four", "masked_bank_reference", "additional_identifiers", "category_leaf_name":
		return true
	default:
		return false
	}
}
