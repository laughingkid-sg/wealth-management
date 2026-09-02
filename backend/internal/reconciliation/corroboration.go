package reconciliation

import (
	"fmt"
	"regexp"
	"strings"

	textcurrency "golang.org/x/text/currency"
)

// DeriveAutoEligibility requires facts in the email's textual source itself.
// It intentionally ignores attachment content and citations: an LLM can name
// a path but cannot make absent text corroborate a transaction.
func DeriveAutoEligibility(candidate Candidate, sourceText string) bool {
	if !hasBoundedAccountIdentifier(candidate.AccountEvidence, sourceText) || !hasQualifiedAmount(candidate.OriginalAmountMinor, candidate.OriginalCurrency, sourceText) {
		return false
	}
	return hasBoundedLiteral(candidate.MerchantName, sourceText) || hasReference(candidate.References, sourceText)
}

func hasBoundedAccountIdentifier(evidence AccountEvidence, source string) bool {
	// Only the two typed, user-configurable matching-key classes may support
	// automatic account resolution. Generic identifiers remain audit detail.
	for _, value := range []string{evidence.CardLastFour, evidence.MaskedBankReference} {
		if hasBoundedLiteral(value, source) {
			return true
		}
	}
	return false
}

func hasReference(values []string, source string) bool {
	for _, value := range values {
		if hasBoundedLiteral(value, source) {
			return true
		}
	}
	return false
}

func hasBoundedLiteral(value, source string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	pattern := `(?i)(^|[^[:alnum:]])` + regexp.QuoteMeta(value) + `([^[:alnum:]]|$)`
	matched, err := regexp.MatchString(pattern, source)
	return err == nil && matched
}

func hasQualifiedAmount(minor int64, code, source string) bool {
	if minor <= 0 || !IsISO4217(code) {
		return false
	}
	amount := formatMinorAmount(minor, code)
	if amount == "" {
		return false
	}
	code = strings.ToUpper(code)
	amounts := []string{amount}
	if grouped := groupedAmount(amount); grouped != amount {
		amounts = append(amounts, grouped)
	}
	patterns := make([]string, 0, 6)
	for _, numeric := range amounts {
		patterns = append(patterns, `(?i)\b`+code+`\s*`+regexp.QuoteMeta(numeric)+`\b`, `(?i)\b`+regexp.QuoteMeta(numeric)+`\s*`+code+`\b`)
	}
	if symbol := safeCurrencySymbol(code); symbol != "" {
		for _, numeric := range amounts {
			patterns = append(patterns, `(?i)`+regexp.QuoteMeta(symbol)+`\s*`+regexp.QuoteMeta(numeric)+`\b`)
		}
	}
	for _, pattern := range patterns {
		if matched, _ := regexp.MatchString(pattern, source); matched {
			return true
		}
	}
	return false
}

func formatMinorAmount(minor int64, code string) string {
	unit, err := textcurrency.ParseISO(code)
	if err != nil {
		return ""
	}
	scale, _ := textcurrency.Standard.Rounding(unit)
	divisor := int64(1)
	for i := 0; i < scale; i++ {
		divisor *= 10
	}
	if scale == 0 {
		return fmt.Sprintf("%d", minor)
	}
	return fmt.Sprintf("%d.%0*d", minor/divisor, scale, minor%divisor)
}

func groupedAmount(value string) string {
	parts := strings.Split(value, ".")
	whole := parts[0]
	if len(whole) <= 3 {
		return value
	}
	groups := make([]string, 0)
	for len(whole) > 3 {
		groups = append([]string{whole[len(whole)-3:]}, groups...)
		whole = whole[:len(whole)-3]
	}
	groups = append([]string{whole}, groups...)
	result := strings.Join(groups, ",")
	if len(parts) == 2 {
		result += "." + parts[1]
	}
	return result
}

func safeCurrencySymbol(code string) string {
	switch code {
	case "SGD":
		return "S$"
	case "USD":
		return "US$"
	default:
		return ""
	}
}
