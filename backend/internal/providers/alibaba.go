package providers

import "context"

// ParsedCandidate carries both the model result and the exact JSON request and
// response bodies needed for an owner-visible parse audit. Authentication
// headers are never included.
type ParsedCandidate struct {
	JSON             []byte
	Model            string
	ProviderRequest  []byte
	ProviderResponse []byte
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
	ParseTransactionEvidence(context.Context, string, string, []AttachmentInput) (ParsedCandidate, error)
}
