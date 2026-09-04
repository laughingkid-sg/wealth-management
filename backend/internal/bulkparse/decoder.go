// Package bulkparse defines the strict, versioned model boundary for Bulk Import.
package bulkparse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	SchemaVersion         = 1
	MaxCandidatesPerChunk = 100
	maxEvidencePerResult  = 64
	maxLineItems          = 100
	maxReferences         = 50
	maxStringRunes        = 500
)

var (
	evidencePathPattern = regexp.MustCompile(`^file\[(0|[1-9][0-9]*)\]\.page\[([1-9][0-9]*)\]$`)
	accountRefPattern   = regexp.MustCompile(`^account_[1-9][0-9]{0,2}$`)
	currencyPattern     = regexp.MustCompile(`^[A-Z]{3}$`)
	cardLastFourPattern = regexp.MustCompile(`^[0-9]{4}$`)
)

type DocumentType string

const (
	GenericDocument   DocumentType = "generic_transactions"
	CreditCardBill    DocumentType = "credit_card_bill"
	TimePrecisionDate              = "date"
)

type Response struct {
	SchemaVersion   int                 `json:"schema_version"`
	DocumentSummary *BillSummary        `json:"document_summary"`
	Transactions    []TransactionResult `json:"transactions"`
}

type BillSummary struct {
	CardAccountRef       *string    `json:"card_account_ref"`
	PeriodStart          *string    `json:"period_start"`
	PeriodEnd            *string    `json:"period_end"`
	StatementDate        *string    `json:"statement_date"`
	DueDate              *string    `json:"due_date"`
	SettlementCurrency   *string    `json:"settlement_currency"`
	AmountDueMinor       *int64     `json:"amount_due_minor"`
	MinimumPaymentMinor  *int64     `json:"minimum_payment_minor"`
	PreviousBalanceMinor *int64     `json:"previous_balance_minor"`
	Evidence             []Evidence `json:"evidence"`
}

type TransactionResult struct {
	Candidate Candidate  `json:"candidate"`
	Evidence  []Evidence `json:"evidence"`
}

type Candidate struct {
	LineIndex                int             `json:"line_index"`
	LineKind                 string          `json:"line_kind"`
	TransactionKind          string          `json:"transaction_kind"`
	Title                    string          `json:"title"`
	MerchantName             string          `json:"merchant_name"`
	OriginalAmountMinor      int64           `json:"original_amount_minor"`
	OriginalCurrency         string          `json:"original_currency"`
	SGDAmountMinor           *int64          `json:"sgd_amount_minor"`
	OccurredOn               string          `json:"occurred_on"`
	TimePrecision            string          `json:"time_precision"`
	References               []string        `json:"references"`
	AccountRef               string          `json:"account_ref"`
	AccountEvidence          AccountEvidence `json:"account_evidence"`
	LineItems                []LineItem      `json:"line_items"`
	CategoryLeafName         string          `json:"category_leaf_name"`
	PossibleInternalTransfer bool            `json:"possible_internal_transfer"`
}

type AccountEvidence struct {
	CardLastFour          string   `json:"card_last_four"`
	MaskedBankReference   string   `json:"masked_bank_reference"`
	AdditionalIdentifiers []string `json:"additional_identifiers"`
}

type LineItem struct {
	SchemaVersion  int             `json:"schema_version"`
	Description    string          `json:"description"`
	Quantity       int64           `json:"quantity"`
	UnitPriceMinor *int64          `json:"unit_price_minor"`
	LineTotalMinor *int64          `json:"line_total_minor"`
	TaxMinor       *int64          `json:"tax_minor"`
	DiscountMinor  *int64          `json:"discount_minor"`
	Currency       string          `json:"currency"`
	Details        json.RawMessage `json:"details"`
}

type Evidence struct {
	Field      string  `json:"field"`
	SourcePath string  `json:"source_path"`
	Confidence float64 `json:"confidence"`
}

// DecodeContext contains the server-owned facts needed to validate an LLM
// response. PageManifest is authoritative: model evidence may cite only these
// exact paths, even when another path happens to match the general path shape.
type DecodeContext struct {
	DocumentType       DocumentType
	AllowedAccountRefs []string
	PageManifest       []string
}

// Decode is retained for stored v1 payloads that predate manifest-aware
// validation. New provider responses must use DecodeWithContext.
func Decode(raw []byte, documentType DocumentType, allowedAccountRefs []string) (Response, error) {
	return decode(raw, DecodeContext{DocumentType: documentType, AllowedAccountRefs: allowedAccountRefs}, nil)
}

// DecodeWithContext decodes and validates a new provider response against the
// exact account selection and page manifest supplied by the server.
func DecodeWithContext(raw []byte, input DecodeContext) (Response, error) {
	manifest, err := validatePageManifest(input.PageManifest)
	if err != nil {
		return Response{}, err
	}
	return decode(raw, input, manifest)
}

func decode(raw []byte, input DecodeContext, manifest map[string]struct{}) (Response, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("decode bulk response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Response{}, errors.New("decode bulk response: trailing JSON")
	}
	if response.SchemaVersion != SchemaVersion {
		return Response{}, errors.New("unsupported bulk response schema version")
	}
	if response.Transactions == nil || len(response.Transactions) > MaxCandidatesPerChunk {
		return Response{}, errors.New("bulk response candidate count is invalid")
	}
	if input.DocumentType != GenericDocument && input.DocumentType != CreditCardBill {
		return Response{}, errors.New("bulk document type is invalid")
	}
	allowed := make(map[string]struct{}, len(input.AllowedAccountRefs))
	for _, ref := range input.AllowedAccountRefs {
		if !accountRefPattern.MatchString(ref) {
			return Response{}, errors.New("allowed account reference is invalid")
		}
		if _, duplicate := allowed[ref]; duplicate {
			return Response{}, errors.New("allowed account references contain a duplicate")
		}
		allowed[ref] = struct{}{}
	}
	if len(allowed) == 0 {
		return Response{}, errors.New("at least one allowed account is required")
	}
	soleAccountRef := ""
	if len(allowed) == 1 {
		for ref := range allowed {
			soleAccountRef = ref
		}
		for index := range response.Transactions {
			response.Transactions[index].Candidate.AccountRef = soleAccountRef
		}
		if response.DocumentSummary != nil {
			response.DocumentSummary.CardAccountRef = &soleAccountRef
		}
	}
	if input.DocumentType == CreditCardBill {
		if response.DocumentSummary == nil {
			return Response{}, errors.New("credit card bill summary is required")
		}
		if err := validateBillSummary(*response.DocumentSummary, allowed, soleAccountRef != "", manifest); err != nil {
			return Response{}, err
		}
	} else if response.DocumentSummary != nil {
		return Response{}, errors.New("generic document summary must be null")
	}
	seenLineIndexes := map[int]struct{}{}
	for index := range response.Transactions {
		if err := validateTransaction(response.Transactions[index], input.DocumentType, allowed, soleAccountRef != "", manifest); err != nil {
			return Response{}, fmt.Errorf("transaction %d: %w", index, err)
		}
		lineIndex := response.Transactions[index].Candidate.LineIndex
		if input.DocumentType == CreditCardBill {
			if _, duplicate := seenLineIndexes[lineIndex]; duplicate {
				return Response{}, errors.New("credit card line_index must be unique in a chunk")
			}
			seenLineIndexes[lineIndex] = struct{}{}
		}
	}
	return response, nil
}

func validatePageManifest(paths []string) (map[string]struct{}, error) {
	if len(paths) == 0 || len(paths) > 5 {
		return nil, errors.New("bulk page manifest count is invalid")
	}
	manifest := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !evidencePathPattern.MatchString(path) {
			return nil, errors.New("bulk page manifest path is invalid")
		}
		if _, duplicate := manifest[path]; duplicate {
			return nil, errors.New("bulk page manifest contains a duplicate")
		}
		manifest[path] = struct{}{}
	}
	return manifest, nil
}

func validateBillSummary(summary BillSummary, allowed map[string]struct{}, serverBound bool, manifest map[string]struct{}) error {
	required := map[string]bool{}
	allowedEvidence := map[string]bool{}
	if summary.CardAccountRef != nil {
		if _, ok := allowed[*summary.CardAccountRef]; !ok {
			return errors.New("bill summary account_ref is not allowed")
		}
		allowedEvidence["card_account_ref"] = true
		if !serverBound {
			required["card_account_ref"] = true
		}
	} else if !serverBound {
		return errors.New("bill summary account_ref is required when multiple accounts are allowed")
	}
	for name, value := range map[string]*string{
		"period_start": summary.PeriodStart, "period_end": summary.PeriodEnd,
		"statement_date": summary.StatementDate, "due_date": summary.DueDate,
	} {
		if value != nil {
			if _, err := time.Parse("2006-01-02", *value); err != nil {
				return fmt.Errorf("bill summary %s must be a date", name)
			}
			required[name] = true
			allowedEvidence[name] = true
		}
	}
	if summary.PeriodStart != nil && summary.PeriodEnd != nil && *summary.PeriodStart > *summary.PeriodEnd {
		return errors.New("bill period start follows period end")
	}
	if summary.StatementDate != nil && summary.DueDate != nil && *summary.StatementDate > *summary.DueDate {
		return errors.New("statement date follows due date")
	}
	if summary.SettlementCurrency != nil {
		if !currencyPattern.MatchString(*summary.SettlementCurrency) {
			return errors.New("bill settlement currency is invalid")
		}
		required["settlement_currency"] = true
		allowedEvidence["settlement_currency"] = true
	}
	for name, value := range map[string]*int64{
		"amount_due_minor":       summary.AmountDueMinor,
		"minimum_payment_minor":  summary.MinimumPaymentMinor,
		"previous_balance_minor": summary.PreviousBalanceMinor,
	} {
		if value != nil {
			if *value < 0 || (name == "amount_due_minor" && *value == 0) {
				return fmt.Errorf("bill summary %s is invalid", name)
			}
			required[name] = true
			allowedEvidence[name] = true
		}
	}
	return validateEvidence(summary.Evidence, required, allowedEvidence, "document_summary.", manifest)
}

func validateTransaction(result TransactionResult, documentType DocumentType, allowed map[string]struct{}, serverBound bool, manifest map[string]struct{}) error {
	candidate := result.Candidate
	if candidate.TransactionKind != "debit" && candidate.TransactionKind != "credit" {
		return errors.New("transaction_kind is invalid")
	}
	if candidate.OriginalAmountMinor <= 0 || !currencyPattern.MatchString(candidate.OriginalCurrency) ||
		candidate.SGDAmountMinor != nil && *candidate.SGDAmountMinor <= 0 {
		return errors.New("transaction amount or currency is invalid")
	}
	if !boundedRequired(candidate.Title, 250) || !boundedOptional(candidate.MerchantName, 250) ||
		!boundedOptional(candidate.CategoryLeafName, 100) {
		return errors.New("transaction text is invalid")
	}
	if candidate.TimePrecision != TimePrecisionDate {
		return errors.New("time_precision must be date")
	}
	if _, err := time.Parse("2006-01-02", candidate.OccurredOn); err != nil {
		return errors.New("occurred_on must be a calendar date")
	}
	if _, ok := allowed[candidate.AccountRef]; !ok {
		return errors.New("account_ref is not allowed")
	}
	if len(candidate.References) > maxReferences || len(candidate.LineItems) > maxLineItems ||
		len(candidate.AccountEvidence.AdditionalIdentifiers) > 20 {
		return errors.New("transaction arrays exceed limits")
	}
	if candidate.AccountEvidence.CardLastFour != "" && !cardLastFourPattern.MatchString(candidate.AccountEvidence.CardLastFour) {
		return errors.New("card_last_four must contain exactly four digits")
	}
	if !boundedOptional(candidate.AccountEvidence.MaskedBankReference, maxStringRunes) {
		return errors.New("masked bank reference is invalid")
	}
	for _, value := range append(append([]string{}, candidate.References...), candidate.AccountEvidence.AdditionalIdentifiers...) {
		if !boundedRequired(value, maxStringRunes) {
			return errors.New("transaction reference is invalid")
		}
	}
	for _, item := range candidate.LineItems {
		if err := validateLineItem(item, candidate.OriginalCurrency); err != nil {
			return err
		}
	}
	if documentType == CreditCardBill {
		if candidate.LineIndex < 1 || !validBillLineKind(candidate.LineKind) {
			return errors.New("credit card line_index or line_kind is invalid")
		}
		wantKind := "debit"
		if candidate.LineKind == "refund" || candidate.LineKind == "payment" {
			wantKind = "credit"
		}
		if candidate.TransactionKind != wantKind {
			return fmt.Errorf("credit card %s line must be a Card %s", candidate.LineKind, wantKind)
		}
	} else if candidate.LineIndex != 0 || candidate.LineKind != "" {
		return errors.New("generic candidates must not provide bill line fields")
	}
	required := map[string]bool{
		"transaction_kind": true, "title": true, "original_amount_minor": true,
		"original_currency": true, "occurred_on": true,
	}
	allowedEvidence := copyFieldSet(required)
	allowedEvidence["account_ref"] = true
	if !serverBound {
		required["account_ref"] = true
	}
	if candidate.MerchantName != "" {
		required["merchant_name"] = true
		allowedEvidence["merchant_name"] = true
	}
	if candidate.SGDAmountMinor != nil {
		required["sgd_amount_minor"] = true
		allowedEvidence["sgd_amount_minor"] = true
	}
	if len(candidate.References) > 0 {
		required["references"] = true
		allowedEvidence["references"] = true
	}
	if hasAccountEvidence(candidate.AccountEvidence) {
		required["account_evidence"] = true
		allowedEvidence["account_evidence"] = true
	}
	if len(candidate.LineItems) > 0 {
		required["line_items"] = true
		allowedEvidence["line_items"] = true
	}
	if candidate.CategoryLeafName != "" {
		required["category_leaf_name"] = true
		allowedEvidence["category_leaf_name"] = true
	}
	if candidate.PossibleInternalTransfer {
		required["possible_internal_transfer"] = true
		allowedEvidence["possible_internal_transfer"] = true
	}
	if documentType == CreditCardBill {
		required["line_index"], required["line_kind"] = true, true
		allowedEvidence["line_index"], allowedEvidence["line_kind"] = true, true
	}
	return validateEvidence(result.Evidence, required, allowedEvidence, "", manifest)
}

func validateLineItem(item LineItem, currency string) error {
	if item.SchemaVersion != 1 || !boundedRequired(item.Description, 250) || item.Quantity < 1 ||
		item.Currency != currency || len(item.Details) == 0 || !json.Valid(item.Details) {
		return errors.New("line item is invalid")
	}
	var detail map[string]json.RawMessage
	if json.Unmarshal(item.Details, &detail) != nil || detail == nil {
		return errors.New("line item details must be an object")
	}
	for _, amount := range []*int64{item.UnitPriceMinor, item.LineTotalMinor, item.TaxMinor, item.DiscountMinor} {
		if amount != nil && *amount < 0 {
			return errors.New("line item amount is invalid")
		}
	}
	return nil
}

func validateEvidence(evidence []Evidence, required, allowed map[string]bool, prefix string, manifest map[string]struct{}) error {
	if len(evidence) > maxEvidencePerResult {
		return errors.New("evidence exceeds limit")
	}
	seen := make(map[string]bool, len(required))
	for _, item := range evidence {
		if prefix != "" && !strings.HasPrefix(item.Field, prefix) {
			return errors.New("evidence entry is invalid")
		}
		field := strings.TrimPrefix(item.Field, prefix)
		if _, ok := allowed[field]; !ok || !validEvidenceSource(item.SourcePath, manifest) || seen[field] ||
			item.Confidence < 0 || item.Confidence > 1 {
			return errors.New("evidence entry is invalid")
		}
		seen[field] = true
	}
	missing := make([]string, 0)
	for field := range required {
		if !seen[field] {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing evidence for %s", strings.Join(missing, ", "))
	}
	return nil
}

func validEvidenceSource(path string, manifest map[string]struct{}) bool {
	if manifest == nil {
		return evidencePathPattern.MatchString(path)
	}
	_, ok := manifest[path]
	return ok
}

func copyFieldSet(input map[string]bool) map[string]bool {
	result := make(map[string]bool, len(input)+1)
	for field, value := range input {
		result[field] = value
	}
	return result
}

func hasAccountEvidence(value AccountEvidence) bool {
	return value.CardLastFour != "" || value.MaskedBankReference != "" || len(value.AdditionalIdentifiers) > 0
}

func validBillLineKind(value string) bool {
	switch value {
	case "activity", "refund", "fee", "interest", "payment":
		return true
	default:
		return false
	}
}

func boundedRequired(value string, max int) bool {
	return strings.TrimSpace(value) == value && value != "" && utf8.RuneCountInString(value) <= max
}

func boundedOptional(value string, max int) bool { return value == "" || boundedRequired(value, max) }

// FingerprintV1 is stable across page overlap and model wording noise while
// retaining the facts that distinguish canonical transactions.
func FingerprintV1(accountID uuid.UUID, candidate Candidate) (string, error) {
	if accountID == uuid.Nil {
		return "", errors.New("resolved account is required")
	}
	if _, err := time.Parse("2006-01-02", candidate.OccurredOn); err != nil {
		return "", errors.New("candidate date is invalid")
	}
	references := append([]string(nil), candidate.References...)
	for index := range references {
		references[index] = normalizeText(references[index])
	}
	sort.Strings(references)
	canonical := strings.Join([]string{
		"bulk-candidate-fingerprint-v1", accountID.String(), candidate.TransactionKind,
		strconv.FormatInt(candidate.OriginalAmountMinor, 10), candidate.OriginalCurrency,
		candidate.OccurredOn, strings.Join(references, "\x1e"), normalizeText(candidate.MerchantName), normalizeText(candidate.Title),
	}, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:]), nil
}

func normalizeText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

type Deduped struct {
	Index       int
	Fingerprint string
	DuplicateOf *int
}

func DedupeV1(accountIDs []uuid.UUID, candidates []Candidate) ([]Deduped, error) {
	if len(accountIDs) != len(candidates) {
		return nil, errors.New("one resolved account is required per candidate")
	}
	first := map[string]int{}
	result := make([]Deduped, len(candidates))
	for index, candidate := range candidates {
		fingerprint, err := FingerprintV1(accountIDs[index], candidate)
		if err != nil {
			return nil, err
		}
		result[index] = Deduped{Index: index, Fingerprint: fingerprint}
		if original, duplicate := first[fingerprint]; duplicate {
			copy := original
			result[index].DuplicateOf = &copy
		} else {
			first[fingerprint] = index
		}
	}
	return result, nil
}
