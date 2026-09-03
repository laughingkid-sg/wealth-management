// Package reconciliation contains pure domain rules for turning parsed source
// evidence into a transaction action. It deliberately has no HTTP, database,
// provider, or LLM dependencies.
package reconciliation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	textcurrency "golang.org/x/text/currency"
)

var (
	ruleEvidencePath  = regexp.MustCompile(`^rule:[^:\s]+:v[1-9][0-9]*$`)
	modelEvidencePath = regexp.MustCompile(`^(?:received_at|(?:subject|sender|text|attachment)(?:\.[A-Za-z0-9_-]+|\[[0-9]+\])*)$`)
)

const (
	// MatchWindow is the inclusive maximum distance between source evidence and
	// an existing transaction considered for automatic pairing.
	MatchWindow = 10 * time.Minute

	minimumCreateConfidence     = 0.75
	maxTransactionLineItems     = 100
	maxLineItemDescriptionRunes = 250
	maxSerializedLineItemsBytes = 256 * 1024
)

// TransactionKind describes the direction relative to the linked account.
type TransactionKind string

const (
	KindDebit  TransactionKind = "debit"
	KindCredit TransactionKind = "credit"
)

// Outcome is the next reconciliation action for a parsed source.
type Outcome string

const (
	OutcomeAttach   Outcome = "attach"
	OutcomeCreate   Outcome = "create"
	OutcomeReview   Outcome = "review"
	OutcomeDangling Outcome = "dangling"
)

// Candidate is normalized transaction evidence from one data source. IDs are
// strings so callers may use UUIDs without coupling this package to a storage driver.
type Candidate struct {
	// UserID is trusted server context, never model output. The worker binds it
	// after strict decoding and before persistence/reconciliation.
	UserID              string          `json:"-"`
	Kind                TransactionKind `json:"transaction_kind"`
	Title               string          `json:"title"`
	MerchantName        string          `json:"merchant_name"`
	OriginalAmountMinor int64           `json:"original_amount_minor"`
	OriginalCurrency    string          `json:"original_currency"`
	SGDAmountMinor      *int64          `json:"sgd_amount_minor,omitempty"`
	OccurredAt          time.Time       `json:"occurred_at"`
	References          []string        `json:"references"`
	AccountEvidence     AccountEvidence `json:"account_evidence"`
	LineItems           []LineItem      `json:"line_items"`
	CategoryLeafName    string          `json:"category_leaf_name,omitempty"`
	// Confidence is derived server-side from valid citations, never accepted
	// from model output.
	Confidence float64 `json:"-"`
	// AutoEligible is server-derived corroboration, never model output.
	AutoEligible bool `json:"-"`
}

// AccountEvidence contains only source-derived safe identifiers, never a full
// card or bank-account number. The values may be masked (for example, ****1234).
type AccountEvidence struct {
	CardLastFour          string   `json:"card_last_four"`
	MaskedBankReference   string   `json:"masked_bank_reference"`
	AdditionalIdentifiers []string `json:"additional_identifiers"`
}

// AccountIdentity contains only explicitly configured matching keys for an
// account owned by the current user. Account names, generic identifiers and
// metadata are deliberately excluded from automatic matching.
type AccountIdentity struct {
	ID           string
	UserID       string
	MatchingKeys []AccountMatchingKey
}

type AccountMatchingKey struct {
	KeyType         string
	NormalizedValue string
}

// LineItem is the supported, versioned shape of one transaction line item.
// Amount fields are minor units and, when present, must be non-negative.
type LineItem struct {
	SchemaVersion  int             `json:"schema_version"`
	Description    string          `json:"description"`
	Quantity       int             `json:"quantity"`
	UnitPriceMinor *int64          `json:"unit_price_minor,omitempty"`
	LineTotalMinor *int64          `json:"line_total_minor,omitempty"`
	TaxMinor       *int64          `json:"tax_minor,omitempty"`
	DiscountMinor  *int64          `json:"discount_minor,omitempty"`
	Currency       string          `json:"currency"`
	Details        json.RawMessage `json:"details"`
}

// Transaction is the canonical data considered as a possible evidence match.
type Transaction struct {
	ID                  string
	UserID              string
	AccountID           string
	Kind                TransactionKind
	MerchantName        string
	OriginalAmountMinor int64
	OriginalCurrency    string
	OccurredAt          time.Time
	References          []string
}

// MatchScore shows the explainable contribution of every matching signal.
// AccountScore is present only after safe account evidence resolved one account.
type MatchScore struct {
	AccountScore   int
	ReferenceScore int
	AmountScore    int
	CurrencyScore  int
	MerchantScore  int
	TimeScore      int
}

func (s MatchScore) Total() int {
	return s.AccountScore + s.ReferenceScore + s.AmountScore + s.CurrencyScore + s.MerchantScore + s.TimeScore
}

// Decision is an audit-friendly reconciliation result. TransactionID is set
// only for an attach result; AccountID is set when the evidence resolved one.
type Decision struct {
	Outcome       Outcome
	Reason        string
	AccountID     string
	TransactionID string
	Score         MatchScore
}

// CandidateValidator permits application adapters to validate deterministic
// or LLM-derived candidates before persistence.
type CandidateValidator interface {
	ValidateCandidate(Candidate) error
}

// ParsedResponseValidator validates the structured response supplied by a
// parser. Provider calls and JSON decoding live outside this package.
type ParsedResponseValidator interface {
	ValidateParsedResponse(ParsedResponse) error
}

// ParsedResponse is the typed, decoded LLM output. Each populated canonical
// field must cite its source path in the original provider payload.
type ParsedResponse struct {
	Candidate Candidate       `json:"candidate"`
	Evidence  []FieldEvidence `json:"evidence"`
}

// FieldEvidence records a non-empty source path and confidence for a parsed field.
type FieldEvidence struct {
	Field      string  `json:"field"`
	SourcePath string  `json:"source_path"`
	Confidence float64 `json:"confidence"`
}

// Validator implements the domain validation interfaces without retaining state.
type Validator struct{}

func (Validator) ValidateCandidate(candidate Candidate) error { return ValidateCandidate(candidate) }
func (Validator) ValidateParsedResponse(response ParsedResponse) error {
	return ValidateParsedResponse(response)
}

// ValidateCandidate verifies facts that must be true before reconciliation.
// Lack of account evidence is valid: it leads to a dangling source instead of
// an unlinked canonical transaction.
func ValidateCandidate(candidate Candidate) error {
	var problems []string
	if strings.TrimSpace(candidate.UserID) == "" {
		problems = append(problems, "user ID is required")
	}
	if candidate.Kind != KindDebit && candidate.Kind != KindCredit {
		problems = append(problems, "transaction kind must be debit or credit")
	}
	if strings.TrimSpace(candidate.Title) == "" {
		problems = append(problems, "title is required")
	}
	if candidate.OriginalAmountMinor <= 0 {
		problems = append(problems, "original amount must be positive minor units")
	}
	if !validCurrency(candidate.OriginalCurrency) {
		problems = append(problems, "original currency must be a three-letter uppercase ISO code")
	}
	if candidate.SGDAmountMinor != nil && *candidate.SGDAmountMinor <= 0 {
		problems = append(problems, "SGD amount must be positive minor units when supplied")
	}
	if candidate.OccurredAt.IsZero() {
		problems = append(problems, "occurred time is required")
	}
	if !validConfidence(candidate.Confidence) {
		problems = append(problems, "confidence must be between zero and one")
	}
	for _, reference := range candidate.References {
		if strings.TrimSpace(reference) == "" {
			problems = append(problems, "references cannot contain an empty value")
			break
		}
	}
	if err := ValidateLineItems(candidate.LineItems); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// ValidateLineItems enforces the transaction-level collection bound and the
// versioned contract for every item before any database write is attempted.
func ValidateLineItems(items []LineItem) error {
	if len(items) > maxTransactionLineItems {
		return fmt.Errorf("line_items must contain at most %d items", maxTransactionLineItems)
	}
	var problems []string
	for index, item := range items {
		if err := ValidateLineItem(item); err != nil {
			problems = append(problems, fmt.Sprintf("line_items[%d]: %v", index, err))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	serialized, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("line_items could not be serialized: %w", err)
	}
	if len(serialized) > maxSerializedLineItemsBytes {
		return fmt.Errorf("serialized line_items must not exceed %d bytes", maxSerializedLineItemsBytes)
	}
	return nil
}

// ValidateLineItem checks the version-one JSON contract after it is decoded.
func ValidateLineItem(item LineItem) error {
	var problems []string
	if item.SchemaVersion != 1 {
		problems = append(problems, "schema version must be 1")
	}
	description := strings.TrimSpace(item.Description)
	if description == "" {
		problems = append(problems, "description is required")
	} else if utf8.RuneCountInString(description) > maxLineItemDescriptionRunes {
		problems = append(problems, fmt.Sprintf("description must be at most %d characters", maxLineItemDescriptionRunes))
	}
	if item.Quantity <= 0 {
		problems = append(problems, "quantity must be a positive integer")
	}
	if !validCurrency(item.Currency) {
		problems = append(problems, "currency must be a three-letter uppercase ISO code")
	}
	for _, amount := range []*int64{item.UnitPriceMinor, item.LineTotalMinor, item.TaxMinor, item.DiscountMinor} {
		if amount != nil && *amount < 0 {
			problems = append(problems, "line-item money fields cannot be negative")
			break
		}
	}
	if !validJSONObject(item.Details) {
		problems = append(problems, "details must be a JSON object")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// ValidateParsedResponse verifies candidate facts and citations from an LLM or
// deterministic parser. It does not make a missing account identifier invalid;
// reconciliation will retain that source as dangling.
func ValidateParsedResponse(response ParsedResponse) error {
	return validateParsedResponse(response, false)
}

// DiscardInvalidOptionalCategoryCitation removes the optional category and all
// of its citations unless every category citation is valid model evidence and
// at least one is present. This recovery is deliberately category-only: bad
// citations for required or other populated fields must still fail validation.
func DiscardInvalidOptionalCategoryCitation(response *ParsedResponse) bool {
	if response == nil {
		return false
	}
	categoryPresent := strings.TrimSpace(response.Candidate.CategoryLeafName) != ""
	citationPresent := false
	citationsValid := true
	for _, evidence := range response.Evidence {
		if evidence.Field != "category_leaf_name" {
			continue
		}
		citationPresent = true
		if !validEvidenceSourcePath(evidence.SourcePath, false) || !validConfidence(evidence.Confidence) {
			citationsValid = false
		}
	}
	if categoryPresent && citationPresent && citationsValid {
		return false
	}

	discarded := categoryPresent || citationPresent
	response.Candidate.CategoryLeafName = ""
	if citationPresent {
		filtered := response.Evidence[:0]
		for _, evidence := range response.Evidence {
			if evidence.Field != "category_leaf_name" {
				filtered = append(filtered, evidence)
			}
		}
		response.Evidence = filtered
	}
	return discarded
}

// ValidateEvidenceEntries validates model-provided citations before any
// trusted deterministic rule evidence is injected.
func ValidateEvidenceEntries(response ParsedResponse) error {
	for _, evidence := range response.Evidence {
		if !decisiveEvidenceField(evidence.Field) {
			return fmt.Errorf("unknown evidence field %q", evidence.Field)
		}
		if !validEvidenceSourcePath(evidence.SourcePath, false) {
			return fmt.Errorf("%s evidence has an invalid source path", evidence.Field)
		}
		if !validConfidence(evidence.Confidence) {
			return fmt.Errorf("%s evidence confidence must be between zero and one", evidence.Field)
		}
	}
	return nil
}

// ValidateParsedResponseAfterRule validates a final parser result after the
// trusted server has injected deterministic rule evidence.
func ValidateParsedResponseAfterRule(response ParsedResponse) error {
	return validateParsedResponse(response, true)
}

func validateParsedResponse(response ParsedResponse, allowRuleEvidence bool) error {
	// Model output deliberately carries no user ID; ownership is bound by the
	// durable job before this result can be persisted.
	candidate := response.Candidate
	candidate.UserID = "bound-by-server"
	if err := ValidateCandidate(candidate); err != nil {
		return err
	}
	required := map[string]bool{
		"transaction_kind":      false,
		"title":                 false,
		"original_amount_minor": false,
		"original_currency":     false,
		"occurred_at":           false,
	}
	if strings.TrimSpace(response.Candidate.MerchantName) != "" {
		required["merchant_name"] = false
	}
	if response.Candidate.SGDAmountMinor != nil {
		required["sgd_amount_minor"] = false
	}
	if len(response.Candidate.References) > 0 {
		required["references"] = false
	}
	if hasAccountEvidence(response.Candidate.AccountEvidence) {
		required["account_evidence"] = false
	}
	if len(response.Candidate.LineItems) > 0 {
		required["line_items"] = false
	}
	if strings.TrimSpace(response.Candidate.CategoryLeafName) != "" {
		required["category_leaf_name"] = false
	}
	for _, evidence := range response.Evidence {
		if !decisiveEvidenceField(evidence.Field) {
			return fmt.Errorf("unknown evidence field %q", evidence.Field)
		}
		if !validEvidenceSourcePath(evidence.SourcePath, allowRuleEvidence) {
			return fmt.Errorf("%s evidence has an invalid source path", evidence.Field)
		}
		if !validConfidence(evidence.Confidence) {
			return fmt.Errorf("%s evidence confidence must be between zero and one", evidence.Field)
		}
		if _, wanted := required[evidence.Field]; wanted {
			required[evidence.Field] = true
		}
	}
	missing := make([]string, 0, len(required))
	for field, cited := range required {
		if !cited {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required field evidence: %s", strings.Join(missing, ", "))
	}
	return nil
}

func decisiveEvidenceField(field string) bool {
	switch field {
	case "transaction_kind", "title", "merchant_name", "original_amount_minor", "original_currency", "sgd_amount_minor", "occurred_at", "references", "account_evidence", "line_items", "category_leaf_name":
		return true
	default:
		return false
	}
}

func validEvidenceSourcePath(path string, allowRule bool) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if allowRule && ruleEvidencePath.MatchString(path) {
		return true
	}
	return modelEvidencePath.MatchString(path)
}

func hasAccountEvidence(evidence AccountEvidence) bool {
	return strings.TrimSpace(evidence.CardLastFour) != "" ||
		strings.TrimSpace(evidence.MaskedBankReference) != "" ||
		len(evidence.AdditionalIdentifiers) > 0
}

// AggregateConfidence uses the minimum valid decisive citation confidence.
// This keeps an extra high-confidence citation from inflating trust in a
// weaker required fact.
func AggregateConfidence(evidence []FieldEvidence) float64 {
	minimum := 1.0
	found := false
	for _, item := range evidence {
		if !decisiveEvidenceField(item.Field) || !validEvidenceSourcePath(item.SourcePath, true) || !validConfidence(item.Confidence) {
			continue
		}
		if !found || item.Confidence < minimum {
			minimum = item.Confidence
		}
		found = true
	}
	if !found {
		return 0
	}
	return minimum
}

// DecodeParsedResponse is a strict boundary for decoded LLM JSON. It rejects
// unknown fields before applying the domain and evidence validation rules.
func DecodeParsedResponse(raw []byte) (ParsedResponse, error) {
	response, err := decodeParsedResponse(raw)
	if err != nil {
		return ParsedResponse{}, err
	}
	if err := ValidateParsedResponse(response); err != nil {
		return ParsedResponse{}, err
	}
	return response, nil
}

// DecodeParsedResponseForRuleApplication is a strict JSON decoder for worker
// use. It deliberately defers required-field validation until trusted rule
// values have been applied; callers must validate evidence first and then call
// ValidateParsedResponseAfterRule.
func DecodeParsedResponseForRuleApplication(raw []byte) (ParsedResponse, error) {
	return decodeParsedResponse(raw)
}

func decodeParsedResponse(raw []byte) (ParsedResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response ParsedResponse
	if err := decoder.Decode(&response); err != nil {
		return ParsedResponse{}, fmt.Errorf("decode parsed response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return ParsedResponse{}, errors.New("decode parsed response: multiple JSON values")
		}
		return ParsedResponse{}, fmt.Errorf("decode parsed response: %w", err)
	}
	return response, nil
}

// Reconcile returns the safe action for a parsed candidate. Account evidence
// must resolve exactly one owned account before attachment or creation is
// possible. Automatic pairing then uses only account, direction, exact amount,
// compatible currency, and the inclusive time window.
func Reconcile(candidate Candidate, accounts []AccountIdentity, transactions []Transaction) (Decision, error) {
	if err := ValidateCandidate(candidate); err != nil {
		return Decision{}, err
	}

	accountID, accountState := resolveAccount(candidate.UserID, candidate.AccountEvidence, accounts)
	switch accountState {
	case accountMissing:
		return Decision{Outcome: OutcomeDangling, Reason: "no account evidence"}, nil
	case accountUnmatched:
		return Decision{Outcome: OutcomeDangling, Reason: "account evidence did not match an owned account"}, nil
	case accountAmbiguous:
		return Decision{Outcome: OutcomeReview, Reason: "account evidence matches more than one account"}, nil
	}

	matches := make([]Transaction, 0)
	for _, transaction := range transactions {
		if transaction.UserID != candidate.UserID || transaction.AccountID != accountID || transaction.Kind != candidate.Kind {
			continue
		}
		if transaction.OriginalAmountMinor != candidate.OriginalAmountMinor ||
			!currenciesCompatible(candidate.OriginalCurrency, transaction.OriginalCurrency) ||
			absoluteDuration(candidate.OccurredAt.Sub(transaction.OccurredAt)) > MatchWindow {
			continue
		}
		matches = append(matches, transaction)
	}

	if len(matches) == 1 {
		match := matches[0]
		return Decision{
			Outcome:       OutcomeAttach,
			AccountID:     accountID,
			TransactionID: match.ID,
			Score:         scoreCandidate(candidate, match),
			Reason:        "unique account, amount, direction, currency, and time match",
		}, nil
	}
	if len(matches) > 1 {
		return Decision{Outcome: OutcomeReview, AccountID: accountID, Reason: "multiple account, amount, direction, currency, and time matches"}, nil
	}
	if !candidate.AutoEligible {
		return Decision{Outcome: OutcomeReview, AccountID: accountID, Reason: "source evidence is not sufficiently corroborated for automatic creation"}, nil
	}
	if candidate.Confidence < minimumCreateConfidence {
		return Decision{Outcome: OutcomeReview, AccountID: accountID, Reason: "candidate confidence is below creation threshold"}, nil
	}
	return Decision{Outcome: OutcomeCreate, AccountID: accountID, Reason: "reliable unmatched candidate"}, nil
}

// currenciesCompatible rejects a pair only when both sides identify different
// currencies. Canonical candidates still require a valid ISO currency; the
// empty-value cases support older or incomplete existing transactions.
func currenciesCompatible(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left == "" || right == "" || strings.EqualFold(left, right)
}

type accountResolution int

const (
	accountMissing accountResolution = iota
	accountUnmatched
	accountAmbiguous
	accountResolved
)

func resolveAccount(userID string, evidence AccountEvidence, accounts []AccountIdentity) (string, accountResolution) {
	keys := evidenceMatchingKeys(evidence)
	if len(keys) == 0 {
		return "", accountMissing
	}
	matched := make(map[string]struct{})
	for _, account := range accounts {
		if account.UserID != userID || strings.TrimSpace(account.ID) == "" {
			continue
		}
		for _, sourceKey := range keys {
			for _, accountKey := range account.MatchingKeys {
				if sourceKey.KeyType == accountKey.KeyType && sourceKey.NormalizedValue == accountKey.NormalizedValue {
					matched[account.ID] = struct{}{}
				}
			}
		}
	}
	if len(matched) == 0 {
		return "", accountUnmatched
	}
	if len(matched) > 1 {
		return "", accountAmbiguous
	}
	for id := range matched {
		return id, accountResolved
	}
	return "", accountUnmatched
}

func evidenceMatchingKeys(evidence AccountEvidence) []AccountMatchingKey {
	values := []struct {
		keyType string
		value   string
	}{
		{keyType: "card_last_four", value: evidence.CardLastFour},
		{keyType: "bank_account_suffix", value: evidence.MaskedBankReference},
	}
	keys := make([]AccountMatchingKey, 0, len(values))
	for _, value := range values {
		normalized, err := NormalizeAccountMatchingKey(value.keyType, value.value)
		if err == nil {
			keys = append(keys, AccountMatchingKey{KeyType: value.keyType, NormalizedValue: normalized})
		}
	}
	return keys
}

// NormalizeAccountMatchingKey is shared by the settings API and automatic
// reconciliation so stored keys and source evidence use exactly one contract.
// Card input is never truncated: a full PAN is rejected rather than reduced to
// its final four digits.
func NormalizeAccountMatchingKey(keyType, value string) (string, error) {
	value = strings.TrimSpace(value)
	switch keyType {
	case "card_last_four":
		var digits strings.Builder
		for _, character := range value {
			switch {
			case character >= '0' && character <= '9':
				digits.WriteRune(character)
			case unicode.IsSpace(character), character == '*', character == '•', character == '-', character == 'x', character == 'X':
				// Common masking characters do not contribute to the key.
			default:
				return "", errors.New("card last four may contain only four digits and masking characters")
			}
		}
		if digits.Len() != 4 {
			return "", errors.New("card last four must contain exactly four digits")
		}
		return digits.String(), nil
	case "bank_account_suffix":
		var normalized strings.Builder
		for _, character := range strings.ToLower(value) {
			if unicode.IsSpace(character) || character == '*' || character == '•' || character == '-' {
				continue
			}
			normalized.WriteRune(character)
		}
		result := normalized.String()
		if result == "" {
			return "", errors.New("bank account suffix is required")
		}
		if utf8.RuneCountInString(result) > 100 {
			return "", errors.New("bank account suffix must be at most 100 characters")
		}
		return result, nil
	default:
		return "", errors.New("matching key type must be card_last_four or bank_account_suffix")
	}
}

func scoreCandidate(candidate Candidate, transaction Transaction) MatchScore {
	score := MatchScore{AccountScore: 50}
	if hasSharedReference(candidate.References, transaction.References) {
		score.ReferenceScore = 60
	}
	if candidate.OriginalAmountMinor == transaction.OriginalAmountMinor {
		score.AmountScore = 25
	}
	if candidate.OriginalCurrency == transaction.OriginalCurrency {
		score.CurrencyScore = 15
	}
	if normalizeMerchant(candidate.MerchantName) != "" && normalizeMerchant(candidate.MerchantName) == normalizeMerchant(transaction.MerchantName) {
		score.MerchantScore = 12
	}
	if absoluteDuration(candidate.OccurredAt.Sub(transaction.OccurredAt)) <= MatchWindow {
		score.TimeScore = 15
	}
	return score
}

func hasSharedReference(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		if normalized := normalizeReference(value); normalized != "" {
			seen[normalized] = struct{}{}
		}
	}
	for _, value := range right {
		if normalized := normalizeReference(value); normalized != "" {
			if _, ok := seen[normalized]; ok {
				return true
			}
		}
	}
	return false
}

func normalizeReference(value string) string { return strings.ToUpper(strings.TrimSpace(value)) }

func normalizeMerchant(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func absoluteDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

// IsISO4217 validates a canonical, uppercase ISO 4217 code using the
// registry rather than accepting arbitrary three-letter strings.
func IsISO4217(value string) bool {
	value = strings.TrimSpace(value)
	if value != strings.ToUpper(value) {
		return false
	}
	_, err := textcurrency.ParseISO(value)
	return err == nil
}

func validCurrency(value string) bool { return IsISO4217(value) }

func validConfidence(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validJSONObject(value json.RawMessage) bool {
	if len(value) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}
