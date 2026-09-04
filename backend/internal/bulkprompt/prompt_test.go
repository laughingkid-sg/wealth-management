package bulkprompt

import (
	"bytes"
	"strings"
	"testing"
)

func TestAssemblePreservesBoundaryAndOmitsPersistentIdentifiers(t *testing.T) {
	assembly, err := Assemble(Input{
		DocumentType: "credit_card_bill", ChunkIndex: 2,
		PageManifest:   []string{"file[0].page[5]", "file[0].page[6]"},
		TemplatePrompt: "Dates use DD/MM/YYYY.",
		Accounts:       []AccountDescriptor{{AccountRef: "account_1", Name: "Rewards Card", Institution: "Citibank", AccountType: "credit_card"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(assembly.SystemPrompt, "Bulk Import platform contract v1") ||
		!strings.Contains(assembly.SystemPrompt, "BEGIN OWNER TEMPLATE GUIDANCE") ||
		!strings.Contains(assembly.SystemPrompt, `"time_precision": "date"`) {
		t.Fatalf("unexpected system prompt: %s", assembly.SystemPrompt)
	}
	if bytes.Contains(assembly.UserMessage, []byte("account_id")) || bytes.Contains(assembly.UserMessage, []byte("user_id")) {
		t.Fatalf("persistent identifier leaked: %s", assembly.UserMessage)
	}
	if len(assembly.VisualPlaceholders) != 2 || !strings.Contains(assembly.VisualPlaceholders[1], "file[0].page[6]") {
		t.Fatalf("unexpected placeholders: %#v", assembly.VisualPlaceholders)
	}
}

func TestAssembleEmbedsExactGenericContract(t *testing.T) {
	assembly, err := Assemble(Input{
		DocumentType: "physical_receipt", ChunkIndex: 0,
		PageManifest:   []string{"file[0].page[1]"},
		TemplatePrompt: "Extract receipt purchases.",
		Accounts:       []AccountDescriptor{{AccountRef: "account_1", Name: "Cash", Institution: "Owner", AccountType: "cash"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, clause := range []string{
		`"document_summary": null`, `"candidate": {`, `"account_evidence": {`,
		`"possible_internal_transfer": false`, `"schema_version": 1`, `"details": {}`,
		"quantity is a positive integer", "source_path must exactly equal one path from document.page_manifest",
		`Never output occurred_at`, `Do not emit evidence for line_index, line_kind, or time_precision`,
		`If exactly one account is allowed`, `If multiple accounts are allowed`,
	} {
		if !strings.Contains(assembly.SystemPrompt, clause) {
			t.Fatalf("system prompt is missing %q", clause)
		}
	}
}

func TestAssembleEmbedsExactCreditCardContract(t *testing.T) {
	assembly, err := Assemble(Input{
		DocumentType: "credit_card_bill", ChunkIndex: 0,
		PageManifest:   []string{"file[0].page[1]"},
		TemplatePrompt: "Read the statement.",
		Accounts:       []AccountDescriptor{{AccountRef: "account_1", Name: "Rewards", Institution: "Bank", AccountType: "credit_card"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, clause := range []string{
		`"card_account_ref": null`, `"amount_due_minor": null`, `"line_index": 1`,
		`activity, refund, fee, interest, or payment`, `document_summary.card_account_ref`,
		`document_summary is required`, `Summary metadata is never itself a transaction`,
	} {
		if !strings.Contains(assembly.SystemPrompt, clause) {
			t.Fatalf("system prompt is missing %q", clause)
		}
	}
}

func TestAssembleRejectsMissingPagesDuplicateRefsAndUnknownProcessor(t *testing.T) {
	base := Input{DocumentType: "other", ChunkIndex: 0, PageManifest: []string{"file[0].page[1]"}, TemplatePrompt: "Extract rows.", Accounts: []AccountDescriptor{{AccountRef: "account_1", Name: "Bank", Institution: "Bank", AccountType: "bank_account"}}}
	missing := base
	missing.PageManifest = nil
	if _, err := Assemble(missing); err == nil {
		t.Fatal("expected missing page error")
	}
	duplicate := base
	duplicate.Accounts = append(duplicate.Accounts, duplicate.Accounts[0])
	if _, err := Assemble(duplicate); err == nil {
		t.Fatal("expected duplicate account ref error")
	}
	unknown := base
	unknown.DocumentType = "instructions_from_document"
	if _, err := Assemble(unknown); err == nil {
		t.Fatal("expected unknown processor error")
	}
}
