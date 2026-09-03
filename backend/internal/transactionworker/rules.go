package transactionworker

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

// applyDeterministicRule overwrites model guesses with validated deterministic
// values and records them as rule evidence.
func applyDeterministicRule(candidate *reconciliation.Candidate, evidence *[]reconciliation.FieldEvidence, rule parserrules.AppliedRule) error {
	if candidate == nil {
		return fmt.Errorf("candidate is nil")
	}
	for field, values := range rule.Values {
		if len(values) == 0 {
			continue
		}
		value := strings.TrimSpace(values[0])
		switch field {
		case "transaction_kind":
			candidate.Kind = reconciliation.TransactionKind(value)
		case "original_amount_minor":
			amount, err := parsePositiveMinor(value)
			if err != nil {
				return err
			}
			candidate.OriginalAmountMinor = amount
		case "original_currency":
			candidate.OriginalCurrency = strings.ToUpper(value)
		case "sgd_amount_minor":
			amount, err := parsePositiveMinor(value)
			if err != nil {
				return err
			}
			candidate.SGDAmountMinor = &amount
		case "occurred_at":
			occurredAt, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return fmt.Errorf("rule occurred_at must be RFC3339: %w", err)
			}
			candidate.OccurredAt = occurredAt.UTC()
		case "merchant_name":
			candidate.MerchantName = value
		case "title":
			candidate.Title = value
		case "references":
			candidate.References = nonEmpty(values)
		case "card_last_four":
			candidate.AccountEvidence.CardLastFour = value
		case "masked_bank_reference":
			candidate.AccountEvidence.MaskedBankReference = value
		case "additional_identifiers":
			candidate.AccountEvidence.AdditionalIdentifiers = nonEmpty(values)
		case "category_leaf_name":
			candidate.CategoryLeafName = value
		}
		*evidence = replaceRuleEvidence(*evidence, field, rule)
	}
	return nil
}

func parsePositiveMinor(value string) (int64, error) {
	amount, err := strconv.ParseInt(value, 10, 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("must be positive integer minor units")
	}
	return amount, nil
}
func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
func replaceRuleEvidence(evidence []reconciliation.FieldEvidence, field string, rule parserrules.AppliedRule) []reconciliation.FieldEvidence {
	field = canonicalEvidenceField(field)
	filtered := evidence[:0]
	for _, item := range evidence {
		if item.Field != field {
			filtered = append(filtered, item)
		}
	}
	return append(filtered, reconciliation.FieldEvidence{Field: field, SourcePath: "rule:" + rule.ID + ":v" + strconv.Itoa(rule.Version), Confidence: 1})
}

func canonicalEvidenceField(field string) string {
	switch field {
	case "card_last_four", "masked_bank_reference", "additional_identifiers":
		return "account_evidence"
	default:
		return field
	}
}
