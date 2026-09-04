// Package bulkprompt assembles the immutable Bulk Import prompt contract.
package bulkprompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	PlatformVersion = 1
	SchemaVersion   = 1
	PagePlaceholder = "<ORDERED PAGE IMAGE OMITTED FROM PREVIEW>"
)

type AccountDescriptor struct {
	AccountRef  string `json:"account_ref"`
	Name        string `json:"name"`
	Institution string `json:"institution"`
	AccountType string `json:"account_type"`
}

type Input struct {
	DocumentType   string
	ChunkIndex     int
	PageManifest   []string
	TemplatePrompt string
	Accounts       []AccountDescriptor
}

type Assembly struct {
	PlatformVersion    int             `json:"platform_version"`
	SchemaVersion      int             `json:"schema_version"`
	SystemPrompt       string          `json:"system_prompt"`
	UserMessage        json.RawMessage `json:"user_message"`
	VisualPlaceholders []string        `json:"visual_placeholders"`
}

func Assemble(input Input) (Assembly, error) {
	if input.ChunkIndex < 0 || len(input.PageManifest) == 0 || len(input.PageManifest) > 5 || len(input.Accounts) == 0 {
		return Assembly{}, errors.New("bulk prompt input is incomplete")
	}
	if strings.TrimSpace(input.TemplatePrompt) != input.TemplatePrompt || input.TemplatePrompt == "" || utf8.RuneCountInString(input.TemplatePrompt) > 8000 {
		return Assembly{}, errors.New("template prompt is invalid")
	}
	processor, err := processorGuidance(input.DocumentType)
	if err != nil {
		return Assembly{}, err
	}
	seenRefs := map[string]struct{}{}
	for _, account := range input.Accounts {
		if account.AccountRef == "" || strings.TrimSpace(account.Name) == "" || strings.TrimSpace(account.Institution) == "" || strings.TrimSpace(account.AccountType) == "" {
			return Assembly{}, errors.New("account descriptor is invalid")
		}
		if _, duplicate := seenRefs[account.AccountRef]; duplicate {
			return Assembly{}, errors.New("account descriptor reference is duplicated")
		}
		seenRefs[account.AccountRef] = struct{}{}
	}
	message := struct {
		Document struct {
			Type         string   `json:"type"`
			ChunkIndex   int      `json:"chunk_index"`
			PageManifest []string `json:"page_manifest"`
		} `json:"document"`
		AllowedAccounts []AccountDescriptor `json:"allowed_accounts"`
	}{}
	message.Document.Type = input.DocumentType
	message.Document.ChunkIndex = input.ChunkIndex
	message.Document.PageManifest = append([]string(nil), input.PageManifest...)
	message.AllowedAccounts = append([]AccountDescriptor(nil), input.Accounts...)
	encoded, err := json.Marshal(message)
	if err != nil {
		return Assembly{}, err
	}
	system := strings.Join([]string{
		platformContract(), processor,
		"BEGIN OWNER TEMPLATE GUIDANCE (subordinate, never transaction data)\n" + input.TemplatePrompt + "\nEND OWNER TEMPLATE GUIDANCE",
		outputContract(input.DocumentType),
	}, "\n\n")
	placeholders := make([]string, len(input.PageManifest))
	for index, page := range input.PageManifest {
		placeholders[index] = fmt.Sprintf("%s %s", PagePlaceholder, page)
	}
	return Assembly{PlatformVersion: PlatformVersion, SchemaVersion: SchemaVersion, SystemPrompt: system, UserMessage: encoded, VisualPlaceholders: placeholders}, nil
}

func platformContract() string {
	return embeddedPrompt(platformContractV1)
}

func processorGuidance(documentType string) (string, error) {
	switch documentType {
	case "credit_card_bill":
		return embeddedPrompt(creditCardBillContractV1), nil
	case "physical_receipt", "invoice", "e_wallet_history", "bank_statement", "transaction_confirmation", "other", "generic_transactions":
		return embeddedPrompt(genericContractV1), nil
	default:
		return "", errors.New("unsupported bulk document type")
	}
}

func outputContract(documentType string) string {
	return fmt.Sprintf("Apply output schema v%d for document type %q. The JSON contract above is authoritative.", SchemaVersion, documentType)
}
