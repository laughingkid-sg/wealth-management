package bulkparse

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDecodeCreditCardBillRequiresTypedLinesDatesAndSummaryEvidence(t *testing.T) {
	raw := validBillResponse(t)
	response, err := Decode(raw, CreditCardBill, []string{"account_1"})
	if err != nil {
		t.Fatal(err)
	}
	if response.DocumentSummary == nil || len(response.Transactions) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	candidate := response.Transactions[0].Candidate
	if candidate.LineIndex != 1 || candidate.LineKind != "activity" || candidate.TimePrecision != "date" || candidate.OccurredOn != "2026-08-04" {
		t.Fatalf("unexpected bill candidate: %#v", candidate)
	}
}

func TestDecodeRejectsUnknownTrailingAndUncitedFields(t *testing.T) {
	valid := validBillResponse(t)
	var object map[string]any
	object = nil
	if err := json.Unmarshal(valid, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpected"] = true
	unknown, _ := json.Marshal(object)
	if _, err := Decode(unknown, CreditCardBill, []string{"account_1"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
	if _, err := Decode(append(valid, []byte(` {}`)...), CreditCardBill, []string{"account_1"}); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing error = %v", err)
	}
	object = nil
	if err := json.Unmarshal(valid, &object); err != nil {
		t.Fatal(err)
	}
	transactions := object["transactions"].([]any)
	result := transactions[0].(map[string]any)
	evidence := result["evidence"].([]any)
	result["evidence"] = evidence[:len(evidence)-1]
	uncited, _ := json.Marshal(object)
	if _, err := Decode(uncited, CreditCardBill, []string{"account_1"}); err == nil || !strings.Contains(err.Error(), "missing evidence") {
		t.Fatalf("missing-evidence error = %v", err)
	}
}

func TestDecodeRejectsTimestampPrecisionAndUnknownAccount(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal(validBillResponse(t), &object); err != nil {
		t.Fatal(err)
	}
	result := object["transactions"].([]any)[0].(map[string]any)
	candidate := result["candidate"].(map[string]any)
	candidate["time_precision"] = "timestamp"
	candidate["occurred_on"] = "2026-08-04T12:00:00Z"
	raw, _ := json.Marshal(object)
	if _, err := Decode(raw, CreditCardBill, []string{"account_1"}); err == nil || !strings.Contains(err.Error(), "time_precision") {
		t.Fatalf("precision error = %v", err)
	}
	candidate["time_precision"] = "date"
	candidate["occurred_on"] = "2026-08-04"
	candidate["account_ref"] = "account_3"
	raw, _ = json.Marshal(object)
	if _, err := Decode(raw, CreditCardBill, []string{"account_1", "account_2"}); err == nil || !strings.Contains(err.Error(), "account_ref") {
		t.Fatalf("account error = %v", err)
	}
}

func TestDecodeGenericRequiresNullSummaryAndNoBillLineFields(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal(validBillResponse(t), &object); err != nil {
		t.Fatal(err)
	}
	object["document_summary"] = nil
	result := object["transactions"].([]any)[0].(map[string]any)
	candidate := result["candidate"].(map[string]any)
	delete(candidate, "line_index")
	delete(candidate, "line_kind")
	evidence := result["evidence"].([]any)
	result["evidence"] = withoutEvidenceFields(evidence, "line_index", "line_kind")
	raw, _ := json.Marshal(object)
	if _, err := Decode(raw, GenericDocument, []string{"account_1"}); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeWithContextBindsSoleAccountWithoutAccountEvidence(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal(validBillResponse(t), &object); err != nil {
		t.Fatal(err)
	}
	summary := object["document_summary"].(map[string]any)
	summary["card_account_ref"] = "account_99"
	summary["evidence"] = withoutEvidenceFields(summary["evidence"].([]any), "document_summary.card_account_ref")
	result := object["transactions"].([]any)[0].(map[string]any)
	result["candidate"].(map[string]any)["account_ref"] = "account_99"
	result["evidence"] = withoutEvidenceFields(result["evidence"].([]any), "account_ref")
	raw, _ := json.Marshal(object)

	response, err := DecodeWithContext(raw, DecodeContext{
		DocumentType: CreditCardBill, AllowedAccountRefs: []string{"account_1"},
		PageManifest: []string{"file[0].page[1]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Transactions[0].Candidate.AccountRef != "account_1" || response.DocumentSummary == nil ||
		response.DocumentSummary.CardAccountRef == nil || *response.DocumentSummary.CardAccountRef != "account_1" {
		t.Fatalf("sole account was not server-bound: %#v", response)
	}
}

func TestDecodeWithContextRequiresCitedAccountSelectionForMultipleAccounts(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal(validGenericResponse(t, "account_2", "file[0].page[1]"), &object); err != nil {
		t.Fatal(err)
	}
	result := object["transactions"].([]any)[0].(map[string]any)
	result["evidence"] = withoutEvidenceFields(result["evidence"].([]any), "account_ref")
	uncited, _ := json.Marshal(object)
	input := DecodeContext{
		DocumentType: GenericDocument, AllowedAccountRefs: []string{"account_1", "account_2"},
		PageManifest: []string{"file[0].page[1]"},
	}
	if _, err := DecodeWithContext(uncited, input); err == nil || !strings.Contains(err.Error(), "account_ref") {
		t.Fatalf("missing account evidence error = %v", err)
	}
	if _, err := DecodeWithContext(validGenericResponse(t, "account_2", "file[0].page[1]"), input); err != nil {
		t.Fatalf("cited account selection rejected: %v", err)
	}
}

func TestDecodeWithContextRequiresEvidencePathInActualManifest(t *testing.T) {
	raw := validGenericResponse(t, "", "file[0].page[2]")
	_, err := DecodeWithContext(raw, DecodeContext{
		DocumentType: GenericDocument, AllowedAccountRefs: []string{"account_1"},
		PageManifest: []string{"file[0].page[1]"},
	})
	if err == nil || !strings.Contains(err.Error(), "evidence entry is invalid") {
		t.Fatalf("off-manifest evidence error = %v", err)
	}
	for _, manifest := range [][]string{nil, {"file[0].page[0]"}, {"file[0].page[1]", "file[0].page[1]"}} {
		if _, err := DecodeWithContext(validGenericResponse(t, "", "file[0].page[1]"), DecodeContext{
			DocumentType: GenericDocument, AllowedAccountRefs: []string{"account_1"}, PageManifest: manifest,
		}); err == nil || !strings.Contains(err.Error(), "manifest") {
			t.Fatalf("invalid manifest %#v error = %v", manifest, err)
		}
	}
}

func TestDecodeWithContextAcceptsReceiptLineItemsAndBindsAccount(t *testing.T) {
	response, err := DecodeWithContext(validGenericResponse(t, "", "file[3].page[7]"), DecodeContext{
		DocumentType: GenericDocument, AllowedAccountRefs: []string{"account_7"},
		PageManifest: []string{"file[3].page[7]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := response.Transactions[0].Candidate
	if candidate.AccountRef != "account_7" || len(candidate.LineItems) != 1 || candidate.LineItems[0].Quantity != 2 {
		t.Fatalf("unexpected receipt candidate: %#v", candidate)
	}
}

func TestDecodeRejectsUnprefixedSummaryEvidence(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal(validBillResponse(t), &object); err != nil {
		t.Fatal(err)
	}
	summary := object["document_summary"].(map[string]any)
	entry := summary["evidence"].([]any)[0].(map[string]any)
	entry["field"] = "card_account_ref"
	raw, _ := json.Marshal(object)
	if _, err := DecodeWithContext(raw, DecodeContext{
		DocumentType: CreditCardBill, AllowedAccountRefs: []string{"account_1"}, PageManifest: []string{"file[0].page[1]"},
	}); err == nil || !strings.Contains(err.Error(), "evidence entry is invalid") {
		t.Fatalf("unprefixed summary evidence error = %v", err)
	}
}

func TestDecodeCreditCardLineKindEnforcesCardDirection(t *testing.T) {
	tests := []struct {
		lineKind string
		wantKind string
	}{
		{lineKind: "activity", wantKind: "debit"},
		{lineKind: "fee", wantKind: "debit"},
		{lineKind: "interest", wantKind: "debit"},
		{lineKind: "refund", wantKind: "credit"},
		{lineKind: "payment", wantKind: "credit"},
	}
	for _, test := range tests {
		t.Run(test.lineKind, func(t *testing.T) {
			var object map[string]any
			if err := json.Unmarshal(validBillResponse(t), &object); err != nil {
				t.Fatal(err)
			}
			candidate := object["transactions"].([]any)[0].(map[string]any)["candidate"].(map[string]any)
			candidate["line_kind"] = test.lineKind
			candidate["transaction_kind"] = test.wantKind
			raw, _ := json.Marshal(object)
			if _, err := Decode(raw, CreditCardBill, []string{"account_1"}); err != nil {
				t.Fatalf("valid mapping rejected: %v", err)
			}
			if test.wantKind == "debit" {
				candidate["transaction_kind"] = "credit"
			} else {
				candidate["transaction_kind"] = "debit"
			}
			raw, _ = json.Marshal(object)
			if _, err := Decode(raw, CreditCardBill, []string{"account_1"}); err == nil || !strings.Contains(err.Error(), "Card "+test.wantKind) {
				t.Fatalf("invalid mapping error = %v", err)
			}
		})
	}
}

func TestFingerprintAndDedupeAreStableForNormalizedTextAndReferences(t *testing.T) {
	accountID := uuid.New()
	left := validCandidate()
	right := validCandidate()
	right.Title = "  EXAMPLE   MERCHANT "
	right.MerchantName = "EXAMPLE merchant"
	right.References = []string{" ref-2 ", "REF-1"}
	left.References = []string{"ref-1", "ref-2"}
	leftHash, err := FingerprintV1(accountID, left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := FingerprintV1(accountID, right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("hashes differ: %s %s", leftHash, rightHash)
	}
	result, err := DedupeV1([]uuid.UUID{accountID, accountID}, []Candidate{left, right})
	if err != nil {
		t.Fatal(err)
	}
	if result[0].DuplicateOf != nil || result[1].DuplicateOf == nil || *result[1].DuplicateOf != 0 {
		t.Fatalf("unexpected dedupe: %#v", result)
	}
}

func validBillResponse(t *testing.T) []byte {
	t.Helper()
	candidate := validCandidate()
	required := []string{"transaction_kind", "title", "merchant_name", "original_amount_minor", "original_currency", "occurred_on", "account_ref", "line_index", "line_kind"}
	evidence := make([]Evidence, 0, len(required))
	for _, field := range required {
		evidence = append(evidence, Evidence{Field: field, SourcePath: "file[0].page[1]", Confidence: .98})
	}
	account := "account_1"
	periodStart, periodEnd, statementDate, dueDate, currency := "2026-08-01", "2026-08-31", "2026-09-01", "2026-09-25", "SGD"
	amount := int64(1250)
	summary := &BillSummary{
		CardAccountRef: &account, PeriodStart: &periodStart, PeriodEnd: &periodEnd,
		StatementDate: &statementDate, DueDate: &dueDate, SettlementCurrency: &currency,
		AmountDueMinor: &amount,
	}
	for _, field := range []string{"card_account_ref", "period_start", "period_end", "statement_date", "due_date", "settlement_currency", "amount_due_minor"} {
		summary.Evidence = append(summary.Evidence, Evidence{Field: "document_summary." + field, SourcePath: "file[0].page[1]", Confidence: .99})
	}
	raw, err := json.Marshal(Response{SchemaVersion: 1, DocumentSummary: summary, Transactions: []TransactionResult{{Candidate: candidate, Evidence: evidence}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validGenericResponse(t *testing.T, accountRef, sourcePath string) []byte {
	t.Helper()
	unit, total := int64(625), int64(1250)
	candidate := Candidate{
		TransactionKind: "debit", Title: "Coffee beans", MerchantName: "Example merchant",
		OriginalAmountMinor: 1250, OriginalCurrency: "SGD", OccurredOn: "2026-08-04",
		TimePrecision: TimePrecisionDate, References: []string{}, AccountRef: accountRef,
		AccountEvidence: AccountEvidence{AdditionalIdentifiers: []string{}},
		LineItems: []LineItem{{SchemaVersion: 1, Description: "Coffee beans", Quantity: 2,
			UnitPriceMinor: &unit, LineTotalMinor: &total, Currency: "SGD", Details: json.RawMessage(`{"weight":"250g"}`)}},
	}
	required := []string{"transaction_kind", "title", "merchant_name", "original_amount_minor", "original_currency", "occurred_on", "line_items"}
	if accountRef != "" {
		required = append(required, "account_ref")
	}
	evidence := make([]Evidence, 0, len(required))
	for _, field := range required {
		evidence = append(evidence, Evidence{Field: field, SourcePath: sourcePath, Confidence: .97})
	}
	raw, err := json.Marshal(Response{SchemaVersion: 1, DocumentSummary: nil, Transactions: []TransactionResult{{Candidate: candidate, Evidence: evidence}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func withoutEvidenceFields(evidence []any, fields ...string) []any {
	omit := make(map[string]bool, len(fields))
	for _, field := range fields {
		omit[field] = true
	}
	result := make([]any, 0, len(evidence))
	for _, raw := range evidence {
		entry, ok := raw.(map[string]any)
		if ok {
			if field, _ := entry["field"].(string); omit[field] {
				continue
			}
		}
		result = append(result, raw)
	}
	return result
}

func validCandidate() Candidate {
	return Candidate{
		LineIndex: 1, LineKind: "activity", TransactionKind: "debit", Title: "Example merchant",
		MerchantName: "Example merchant", OriginalAmountMinor: 1250, OriginalCurrency: "SGD",
		OccurredOn: "2026-08-04", TimePrecision: "date", References: []string{}, AccountRef: "account_1",
		AccountEvidence: AccountEvidence{AdditionalIdentifiers: []string{}}, LineItems: []LineItem{},
	}
}
