// Package transactionworker coordinates parser and reconciliation jobs without
// allowing external model calls to overlap database transactions.
package transactionworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type Repository interface {
	LoadSourceParseInput(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SourceParseInput, error)
	SaveParsedSource(context.Context, uuid.UUID, transactionstore.ParsedSourceResult) error
	RecordInvalidSourceParse(context.Context, uuid.UUID, uuid.UUID, string, json.RawMessage, error) error
	RecordFailedSourceParse(context.Context, uuid.UUID, uuid.UUID, string, error) error
	LoadReconciliationInput(context.Context, uuid.UUID, uuid.UUID) (transactionstore.ReconciliationInput, error)
	PersistReconciliation(context.Context, uuid.UUID, transactionstore.ReconciliationResult) error
}

// Handler routes source parsing and reconciliation jobs. Gmail ingestion is
// intentionally composed separately because it has distinct provider secrets.
type Handler struct {
	Repository Repository
	Parser     providers.TransactionParser
}

func (h Handler) Handle(ctx context.Context, job jobs.Job) error {
	switch job.Kind {
	case jobs.KindSourceParse:
		return h.handleSourceParse(ctx, job)
	case jobs.KindReconcile:
		return h.handleReconciliation(ctx, job)
	default:
		return fmt.Errorf("unsupported transaction processing job kind %q", job.Kind)
	}
}

func (h Handler) handleSourceParse(ctx context.Context, job jobs.Job) error {
	if h.Repository == nil || h.Parser == nil {
		return errors.New("transaction parsing handler is not configured")
	}
	sourceID, err := sourceIDFromPayload(job.Payload)
	if err != nil {
		return err
	}
	input, err := h.Repository.LoadSourceParseInput(ctx, job.UserID, sourceID)
	if err != nil {
		return fmt.Errorf("load source for parsing: %w", err)
	}
	modelResult, err := h.Parser.ParseTransactionEvidence(ctx, input.NormalizedContent, nil)
	if err != nil {
		if recordErr := h.Repository.RecordFailedSourceParse(ctx, job.UserID, sourceID, "qwen3.8-flash", err); recordErr != nil {
			return fmt.Errorf("record parser failure: %w", recordErr)
		}
		return fmt.Errorf("parse source: %w", err)
	}
	parsed, err := reconciliation.DecodeParsedResponse(modelResult.JSON)
	if err == nil && parsed.Candidate.UserID != job.UserID.String() {
		err = errors.New("parsed candidate user ID does not match source owner")
	}
	if err != nil {
		if recordErr := h.Repository.RecordInvalidSourceParse(ctx, job.UserID, sourceID, modelResult.Model, modelResult.JSON, err); recordErr != nil {
			return fmt.Errorf("record invalid parser result: %w", recordErr)
		}
		return nil
	}
	if err := h.Repository.SaveParsedSource(ctx, job.UserID, transactionstore.ParsedSourceResult{
		SourceID: sourceID, SyncRunID: job.SyncRunID, Model: modelResult.Model,
		ParsedResponse: parsed, RawResponse: modelResult.JSON,
	}); err != nil {
		return fmt.Errorf("persist parsed source: %w", err)
	}
	return nil
}

func (h Handler) handleReconciliation(ctx context.Context, job jobs.Job) error {
	if h.Repository == nil {
		return errors.New("reconciliation handler is not configured")
	}
	sourceID, err := sourceIDFromPayload(job.Payload)
	if err != nil {
		return err
	}
	input, err := h.Repository.LoadReconciliationInput(ctx, job.UserID, sourceID)
	if err != nil {
		return fmt.Errorf("load reconciliation input: %w", err)
	}
	decision, err := reconciliation.Reconcile(input.Candidate, input.Accounts, input.Transactions)
	if err != nil {
		return fmt.Errorf("reconcile source: %w", err)
	}
	if err := h.Repository.PersistReconciliation(ctx, job.UserID, transactionstore.ReconciliationResult{
		SourceID: input.SourceID, SyncRunID: job.SyncRunID, Candidate: input.Candidate, Decision: decision,
	}); err != nil {
		return fmt.Errorf("persist reconciliation: %w", err)
	}
	return nil
}

func sourceIDFromPayload(payload []byte) (uuid.UUID, error) {
	var decoded struct {
		DataSourceID string `json:"data_source_id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return uuid.Nil, fmt.Errorf("decode transaction job payload: %w", err)
	}
	id, err := uuid.Parse(decoded.DataSourceID)
	if err != nil {
		return uuid.Nil, errors.New("transaction job has invalid data source ID")
	}
	return id, nil
}
