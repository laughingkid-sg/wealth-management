package providers

import "context"

// ParsedCandidate is provider-neutral. JSON is the unmodified JSON object returned
// by the model; domain validation happens before persistence.
type ParsedCandidate struct {
	JSON  []byte
	Model string
}

// AttachmentInput is an already-authorized, decoded visual attachment. PDF
// attachments must be rendered to a supported image before they reach this
// provider; the OpenAI-compatible image_url format only accepts images.
type AttachmentInput struct {
	Filename, MIMEType string
	Content            []byte
}

// TransactionParser implementations must request structured JSON and disable model thinking.
type TransactionParser interface {
	ParseTransactionEvidence(context.Context, string, []AttachmentInput) (ParsedCandidate, error)
}
