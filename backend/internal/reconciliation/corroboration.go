package reconciliation

import (
	"fmt"
	"regexp"
	"strings"

	textcurrency "golang.org/x/text/currency"
)

var semanticCardLastFourPatterns = []*regexp.Regexp{
	// A masked suffix is independently meaningful even when a card brand was
	// removed during source normalization. Require at least two mask symbols so
	// an ordinary bullet or multiplication marker cannot qualify a bare number.
	regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:[*x•][[:space:]-]*){2,}([0-9]{4})(?:[^0-9]|$)`),
	// Compact card/brand labels such as "card: 1234" or "Visa (1234)".
	regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:card|master[[:space:]-]*card|visa|amex|american[[:space:]-]*express)[[:space:]:#()/.-]{0,16}([0-9]{4})(?:[^0-9]|$)`),
	// Explicit suffix language such as "card ending in 1234". Keep the span
	// bounded and on one line so unrelated receipt numbers cannot drift in.
	regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(?:card|master[[:space:]-]*card|visa|amex|american[[:space:]-]*express)[^0-9\r\n]{0,48}(?:ending(?:[[:space:]]+in)?|ends(?:[[:space:]]+in)?|last[[:space:]]+(?:four|4)|suffix)[^0-9\r\n]{0,16}([0-9]{4})(?:[^0-9]|$)`),
}

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
	if hasSemanticallyCorroboratedCardLastFour(evidence.CardLastFour, source) {
		return true
	}
	return hasBoundedLiteral(evidence.MaskedBankReference, source)
}

// SanitizeAccountEvidenceForMatching demotes a syntactically valid card
// suffix unless the source independently presents exactly that suffix in a
// masked-card or explicit card context. Demoted values remain visible as
// audit-only identifiers but can no longer resolve an Account.
func SanitizeAccountEvidenceForMatching(evidence AccountEvidence, source string) AccountEvidence {
	cardLastFour := strings.TrimSpace(evidence.CardLastFour)
	if cardLastFour == "" {
		return evidence
	}
	if _, err := NormalizeAccountMatchingKey("card_last_four", cardLastFour); err != nil {
		// Leave malformed values for normal candidate validation/reconciliation;
		// never preserve a possible full PAN as an additional identifier.
		return evidence
	}
	if hasSemanticallyCorroboratedCardLastFour(cardLastFour, source) {
		return evidence
	}

	evidence.CardLastFour = ""
	for _, existing := range evidence.AdditionalIdentifiers {
		if strings.TrimSpace(existing) == cardLastFour {
			return evidence
		}
	}
	evidence.AdditionalIdentifiers = append(evidence.AdditionalIdentifiers, cardLastFour)
	return evidence
}

func hasSemanticallyCorroboratedCardLastFour(value, source string) bool {
	normalized, err := NormalizeAccountMatchingKey("card_last_four", value)
	if err != nil {
		return false
	}

	contextualValues := make(map[string]struct{})
	for _, pattern := range semanticCardLastFourPatterns {
		for _, match := range pattern.FindAllStringSubmatch(source, -1) {
			if len(match) > 1 {
				contextualValues[match[1]] = struct{}{}
			}
		}
	}
	if len(contextualValues) != 1 {
		// No semantic card context, or conflicting card suffixes, must never
		// authorize automatic account matching.
		return false
	}
	_, matched := contextualValues[normalized]
	return matched
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
