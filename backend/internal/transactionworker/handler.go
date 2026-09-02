// Package transactionworker coordinates parser and reconciliation jobs without
// allowing external model calls to overlap database transactions.
package transactionworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
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
	Repository  Repository
	Parser      providers.TransactionParser
	Attachments interface {
		Download(context.Context, attachmentstorage.ObjectRequest) ([]byte, error)
	}
}

const maxVisualAttachmentBytes = 5 * 1024 * 1024

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
	matchedRule, hasRule := parserrules.MatchAndApply(input.Sender, input.NormalizedContent, input.Rules)
	attachments, usage, err := h.loadParseAttachments(ctx, job.UserID, sourceID, input.Attachments)
	if err != nil {
		return fmt.Errorf("load parse attachments: %w", err)
	}
	modelResult, err := h.Parser.ParseTransactionEvidence(ctx, input.NormalizedContent, attachments)
	if err != nil {
		if recordErr := h.Repository.RecordFailedSourceParse(ctx, job.UserID, sourceID, "qwen3.8-flash", err); recordErr != nil {
			return fmt.Errorf("record parser failure: %w", recordErr)
		}
		return fmt.Errorf("parse source: %w", err)
	}
	parsed, err := reconciliation.DecodeParsedResponseForRuleApplication(modelResult.JSON)
	if err == nil {
		// Model citations can only point at source content; rule: paths are
		// introduced below by trusted server code.
		err = reconciliation.ValidateEvidenceEntries(parsed)
	}
	if err == nil {
		// Ownership is a property of the leased server-side job, never a field
		// requested from or accepted from the model response.
		parsed.Candidate.UserID = job.UserID.String()
		parsed.Candidate.Confidence = reconciliation.AggregateConfidence(parsed.Evidence)
		if hasRule {
			err = applyDeterministicRule(&parsed.Candidate, &parsed.Evidence, matchedRule)
		}
		parsed.Candidate.AutoEligible = reconciliation.DeriveAutoEligibility(parsed.Candidate, input.NormalizedContent)
		if err == nil {
			err = reconciliation.ValidateParsedResponseAfterRule(parsed)
		}
		parsed.Candidate.Confidence = reconciliation.AggregateConfidence(parsed.Evidence)
		if err == nil {
			err = reconciliation.ValidateCandidate(parsed.Candidate)
		}
		if err != nil {
			err = fmt.Errorf("validate server-bound parsed candidate: %w", err)
		}
	}
	if err != nil {
		if recordErr := h.Repository.RecordInvalidSourceParse(ctx, job.UserID, sourceID, modelResult.Model, modelResult.JSON, err); recordErr != nil {
			return fmt.Errorf("record invalid parser result: %w", recordErr)
		}
		return nil
	}
	if err := h.Repository.SaveParsedSource(ctx, job.UserID, transactionstore.ParsedSourceResult{
		SourceID: sourceID, SyncRunID: job.SyncRunID, Model: modelResult.Model,
		ParsedResponse: parsed, RawResponse: canonicalParsedJSON(parsed), AttachmentUsage: usage,
		AutoEligible: parsed.Candidate.AutoEligible,
		RuleID:       ruleID(matchedRule, hasRule), RuleVersion: ruleVersion(matchedRule, hasRule),
	}); err != nil {
		return fmt.Errorf("persist parsed source: %w", err)
	}
	return nil
}

func (h Handler) loadParseAttachments(ctx context.Context, userID, sourceID uuid.UUID, metadata []transactionstore.SourceAttachment) ([]providers.AttachmentInput, []transactionstore.AttachmentUsage, error) {
	if h.Attachments == nil {
		return nil, nil, nil
	}
	attachments := make([]providers.AttachmentInput, 0)
	usage := make([]transactionstore.AttachmentUsage, 0)
	visualBytes := 0
	for _, item := range metadata {
		if len(attachments) >= 5 {
			break
		}
		if item.StorageStatus != "stored" || !item.ParseEligible || !receiptOrInvoice(item.Filename) || strings.TrimSpace(item.ObjectPath) == "" {
			continue
		}
		content, err := h.Attachments.Download(ctx, attachmentstorage.ObjectRequest{UserID: userID, SourceID: sourceID, ObjectPath: item.ObjectPath})
		if err != nil {
			return nil, usage, err
		}
		for _, visual := range renderVisualAttachment(ctx, item.Filename, item.MIMEType, content) {
			if len(attachments) >= 5 {
				break
			}
			if len(visual.Content) == 0 || len(visual.Content) > maxVisualAttachmentBytes || visualBytes > maxVisualAttachmentBytes-len(visual.Content) {
				// Rendered evidence is optional. Do not let a multi-page receipt
				// exceed the parser's aggregate visual budget and fail the source.
				continue
			}
			attachments = append(attachments, visual)
			visualBytes += len(visual.Content)
			usage = append(usage, transactionstore.AttachmentUsage{ObjectPath: item.ObjectPath, Filename: visual.Filename, MIMEType: visual.MIMEType})
		}
	}
	return attachments, usage, nil
}

func receiptOrInvoice(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.Contains(lower, "receipt") || strings.Contains(lower, "invoice")
}

func canonicalParsedJSON(parsed reconciliation.ParsedResponse) json.RawMessage {
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return nil
	}
	return encoded
}

func ruleID(rule parserrules.AppliedRule, present bool) string {
	if !present {
		return ""
	}
	return rule.ID
}
func ruleVersion(rule parserrules.AppliedRule, present bool) int {
	if !present {
		return 0
	}
	return rule.Version
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
