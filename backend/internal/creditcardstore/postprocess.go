package creditcardstore

import (
	"context"
	"fmt"

	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
	"github.com/zhengteck/wealth-builder/backend/internal/creditcard"
)

// PostProcessor adapts the Bulk worker's post-processing boundary to the
// Credit Card application service. Only immutable Bulk identity crosses the
// boundary; the PostgreSQL transaction reloads the pinned header and lines.
type PostProcessor struct {
	service *creditcard.Service
}

func NewPostProcessor(service *creditcard.Service) *PostProcessor {
	return &PostProcessor{service: service}
}

func (p *PostProcessor) ProcessCreditCardBill(ctx context.Context, input bulkimport.PostProcessInput) error {
	if p == nil || p.service == nil {
		return fmt.Errorf("credit card post-processor is not configured")
	}
	if input.DocumentType != bulkimport.DocumentCreditCardBill {
		return fmt.Errorf("%w: post-process document is not a Credit Card bill", creditcard.ErrValidation)
	}
	_, err := p.service.ProjectBulkBill(ctx, input.UserID, input.DocumentID, input.AttemptGeneration)
	return err
}
