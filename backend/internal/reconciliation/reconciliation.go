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
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	// MatchWindow is a supporting signal only; it can never create a match by itself.
	MatchWindow = 10 * time.Minute

	minimumCreateConfidence = 0.75
	highMatchScore          = 90
	ambiguousScoreDelta     = 10
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
	UserID              string          `json:"user_id"`
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
	Confidence          float64         `json:"confidence"`
}

// AccountEvidence contains only source-derived safe identifiers, never a full
// card or bank-account number. The values may be masked (for example, ****1234).
type AccountEvidence struct {
	CardLastFour          string   `json:"card_last_four"`
	MaskedBankReference   string   `json:"masked_bank_reference"`
	AdditionalIdentifiers []string `json:"additional_identifiers"`
}

// AccountIdentity is the safe subset of Account data available to matching.
// It is expected to be supplied only for accounts owned by the current user.
type AccountIdentity struct {
	ID                   string
	UserID               string
	CardLastFour         string
	BankAccountReference string
	AccountIdentifier    string
	MetadataIdentifiers  []string
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
	for i, item := range candidate.LineItems {
		if err := ValidateLineItem(item); err != nil {
			problems = append(problems, fmt.Sprintf("line item %d: %v", i, err))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// ValidateLineItem checks the version-one JSON contract after it is decoded.
func ValidateLineItem(item LineItem) error {
	var problems []string
	if item.SchemaVersion != 1 {
		problems = append(problems, "schema version must be 1")
	}
	if strings.TrimSpace(item.Description) == "" {
		problems = append(problems, "description is required")
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
	if err := ValidateCandidate(response.Candidate); err != nil {
		return err
	}
	required := map[string]bool{
		"transaction_kind":  false,
		"title":             false,
		"original_amount":   false,
		"original_currency": false,
		"occurred_at":       false,
	}
	for _, evidence := range response.Evidence {
		if _, wanted := required[evidence.Field]; wanted {
			if strings.TrimSpace(evidence.SourcePath) == "" {
				return fmt.Errorf("%s evidence must include a source path", evidence.Field)
			}
			if !validConfidence(evidence.Confidence) {
				return fmt.Errorf("%s evidence confidence must be between zero and one", evidence.Field)
			}
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

// DecodeParsedResponse is a strict boundary for decoded LLM JSON. It rejects
// unknown fields before applying the domain and evidence validation rules.
func DecodeParsedResponse(raw []byte) (ParsedResponse, error) {
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
	if err := ValidateParsedResponse(response); err != nil {
		return ParsedResponse{}, err
	}
	return response, nil
}

// Reconcile returns the safe action for a parsed candidate. It never matches
// from merchant, amount, or time alone: account evidence must resolve exactly
// one owned account before attachment or creation is possible.
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

	possible := make([]scoredTransaction, 0)
	for _, transaction := range transactions {
		if transaction.UserID != candidate.UserID || transaction.AccountID != accountID || transaction.Kind != candidate.Kind {
			continue
		}
		score := scoreCandidate(candidate, transaction)
		possible = append(possible, scoredTransaction{transaction: transaction, score: score})
	}
	sort.Slice(possible, func(i, j int) bool { return possible[i].score.Total() > possible[j].score.Total() })

	if len(possible) > 0 && possible[0].score.Total() >= highMatchScore {
		if len(possible) > 1 && possible[0].score.Total()-possible[1].score.Total() <= ambiguousScoreDelta {
			return Decision{Outcome: OutcomeReview, AccountID: accountID, Score: possible[0].score, Reason: "multiple plausible transaction matches"}, nil
		}
		return Decision{Outcome: OutcomeAttach, AccountID: accountID, TransactionID: possible[0].transaction.ID, Score: possible[0].score, Reason: "high-confidence source match"}, nil
	}
	if len(possible) > 0 {
		return Decision{Outcome: OutcomeReview, AccountID: accountID, Score: possible[0].score, Reason: "existing transaction match is below confidence threshold"}, nil
	}
	if candidate.Confidence < minimumCreateConfidence {
		return Decision{Outcome: OutcomeReview, AccountID: accountID, Reason: "candidate confidence is below creation threshold"}, nil
	}
	return Decision{Outcome: OutcomeCreate, AccountID: accountID, Reason: "reliable unmatched candidate"}, nil
}

type accountResolution int

const (
	accountMissing accountResolution = iota
	accountUnmatched
	accountAmbiguous
	accountResolved
)

func resolveAccount(userID string, evidence AccountEvidence, accounts []AccountIdentity) (string, accountResolution) {
	identifiers := evidenceIdentifiers(evidence)
	if len(identifiers) == 0 {
		return "", accountMissing
	}
	matched := make(map[string]struct{})
	for _, account := range accounts {
		if account.UserID != userID || strings.TrimSpace(account.ID) == "" {
			continue
		}
		for _, sourceIdentifier := range identifiers {
			for _, accountIdentifier := range accountIdentifiers(account) {
				if sourceIdentifier == accountIdentifier {
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

func evidenceIdentifiers(evidence AccountEvidence) []string {
	values := append([]string{evidence.CardLastFour, evidence.MaskedBankReference}, evidence.AdditionalIdentifiers...)
	return normalizeIdentifiers(values)
}

func accountIdentifiers(account AccountIdentity) []string {
	values := append([]string{account.CardLastFour, account.BankAccountReference, account.AccountIdentifier}, account.MetadataIdentifiers...)
	return normalizeIdentifiers(values)
}

func normalizeIdentifiers(values []string) []string {
	seen := make(map[string]struct{})
	for _, value := range values {
		lastFour := safeLastFour(value)
		if lastFour != "" {
			seen[lastFour] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func safeLastFour(value string) string {
	digits := make([]rune, 0, 4)
	for _, char := range value {
		if unicode.IsDigit(char) {
			digits = append(digits, char)
		}
	}
	if len(digits) < 4 {
		return ""
	}
	return string(digits[len(digits)-4:])
}

type scoredTransaction struct {
	transaction Transaction
	score       MatchScore
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

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, char := range currency {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

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
