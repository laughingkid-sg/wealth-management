// Package parserrules evaluates versioned global and user-owned source parser
// rules. Go's regexp implementation is RE2-compatible and cannot exhibit
// catastrophic backtracking.
package parserrules

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
)

var ErrAmbiguousTopPriority = errors.New("multiple matching parser rules share the highest priority")

type Rule struct {
	ID               string
	Name             string
	Version          int
	Priority         int
	SenderMatcher    string
	ContentMatcher   string
	PromptFragment   string
	ExtractionConfig json.RawMessage
}

type ExtractionConfig struct {
	Constants  map[string]string       `json:"constants"`
	Extractors map[string]CaptureField `json:"extractors"`
}

type CaptureField struct {
	Pattern string `json:"pattern"`
	Group   int    `json:"group"`
}

type AppliedRule struct {
	ID             string
	Name           string
	Version        int
	Priority       int
	PromptFragment string
	Values         map[string][]string
}

// UserRule is the worker-safe projection of an owner-scoped configurable rule.
type UserRule struct {
	ID               string
	Name             string
	Version          int
	Priority         int
	SenderMatchType  string
	SenderMatchValue string
	SubjectMatcher   string
	ContentMatcher   string
	PromptFragment   string
}

// MatchAndApply returns the single highest-priority matching global rule. Two
// matching rules at the same highest priority are a configuration error rather
// than an arbitrary ID-based choice.
func MatchAndApply(sender, content string, rules []Rule) (AppliedRule, bool, error) {
	sorted := append([]Rule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority == sorted[j].Priority {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Priority > sorted[j].Priority
	})
	var selected AppliedRule
	selectedPresent := false
	for _, rule := range sorted {
		if selectedPresent && rule.Priority < selected.Priority {
			break
		}
		applied, ok := applyOne(sender, content, rule)
		if !ok {
			continue
		}
		if selectedPresent {
			return AppliedRule{}, false, ErrAmbiguousTopPriority
		}
		selected, selectedPresent = applied, true
	}
	return selected, selectedPresent, nil
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
		allMatches := re.FindAllStringSubmatch(content, 10)
		captured := make([]string, 0, len(allMatches))
		for _, match := range allMatches {
			if extractor.Group >= len(match) || strings.TrimSpace(match[extractor.Group]) == "" {
				continue
			}
			captured = append(captured, strings.TrimSpace(match[extractor.Group]))
		}
		if len(captured) > 0 {
			values[field] = captured
		}
	}
	return AppliedRule{
		ID: rule.ID, Name: rule.Name, Version: rule.Version, Priority: rule.Priority,
		PromptFragment: strings.TrimSpace(rule.PromptFragment), Values: values,
	}, true
}

// MatchUserRule evaluates sender, subject and content conditions with AND
// semantics and returns the single highest-priority match.
func MatchUserRule(sender, subject, content string, rules []UserRule) (UserRule, bool, error) {
	sorted := append([]UserRule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority == sorted[j].Priority {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Priority > sorted[j].Priority
	})
	var selected UserRule
	selectedPresent := false
	for _, rule := range sorted {
		if selectedPresent && rule.Priority < selected.Priority {
			break
		}
		matched, err := matchesUserRule(sender, subject, content, rule)
		if err != nil {
			return UserRule{}, false, err
		}
		if !matched {
			continue
		}
		if selectedPresent {
			return UserRule{}, false, ErrAmbiguousTopPriority
		}
		selected, selectedPresent = rule, true
	}
	return selected, selectedPresent, nil
}

func matchesUserRule(sender, subject, content string, rule UserRule) (bool, error) {
	if strings.TrimSpace(rule.ID) == "" || rule.Version < 1 {
		return false, errors.New("user parser rule has invalid provenance")
	}
	matched, err := matchesSender(rule.SenderMatchType, rule.SenderMatchValue, sender)
	if err != nil || !matched {
		return false, err
	}
	for _, condition := range []struct{ pattern, value string }{
		{rule.SubjectMatcher, subject},
		{rule.ContentMatcher, content},
	} {
		if strings.TrimSpace(condition.pattern) == "" {
			continue
		}
		re, compileErr := regexp.Compile(condition.pattern)
		if compileErr != nil {
			return false, fmt.Errorf("invalid configured RE2 expression: %w", compileErr)
		}
		if !re.MatchString(condition.value) {
			return false, nil
		}
	}
	return true, nil
}

func matchesSender(matchType, configured, sender string) (bool, error) {
	configured = strings.TrimSpace(configured)
	switch matchType {
	case "exact":
		return strings.EqualFold(senderAddress(sender), senderAddress(configured)), nil
	case "domain":
		configured = strings.TrimPrefix(strings.ToLower(configured), "@")
		address := senderAddress(sender)
		separator := strings.LastIndexByte(address, '@')
		if separator < 0 {
			return false, nil
		}
		domain := strings.ToLower(address[separator+1:])
		return domain == configured || strings.HasSuffix(domain, "."+configured), nil
	case "regex":
		re, err := regexp.Compile(configured)
		if err != nil {
			return false, fmt.Errorf("invalid configured sender RE2 expression: %w", err)
		}
		return re.MatchString(sender), nil
	default:
		return false, errors.New("invalid configured sender match type")
	}
}

func senderAddress(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := mail.ParseAddress(value); err == nil {
		return strings.ToLower(strings.TrimSpace(parsed.Address))
	}
	return strings.ToLower(value)
}

func ValidateRE2(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return errors.New("RE2 expression is required")
	}
	_, err := regexp.Compile(pattern)
	return err
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
