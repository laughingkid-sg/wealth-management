package providers

import "context"

// ParsedCandidate is provider-neutral; domain validation happens before persistence.
type ParsedCandidate struct {
	JSON  []byte
	Model string
}

type AttachmentInput struct {
	Filename, MIMEType string
	Content            []byte
}

// TransactionParser implementations must request structured JSON and disable model thinking.
type TransactionParser interface {
	ParseTransactionEvidence(context.Context, string, []AttachmentInput) (ParsedCandidate, error)
}
