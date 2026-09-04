// Package transactionworker coordinates parser and reconciliation jobs without
// allowing external model calls to overlap database transactions.
package transactionworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/jobs"
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionprompt"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type Repository interface {
	LoadSourceParseInput(context.Context, uuid.UUID, uuid.UUID) (transactionstore.SourceParseInput, error)
	SaveParsedSource(context.Context, uuid.UUID, transactionstore.ParsedSourceResult) error
	RecordInvalidSourceParse(context.Context, uuid.UUID, transactionstore.SourceParseAudit, error) error
	RecordFailedSourceParse(context.Context, uuid.UUID, transactionstore.SourceParseAudit, error) error
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
	CleanupAttachments interface {
		Delete(context.Context, []attachmentstorage.ObjectRequest) error
	}
}

const (
	maxVisualAttachmentBytes = 5 * 1024 * 1024
	maxCleanupPayloadBytes   = 2 * 1024 * 1024
	maxCleanupObjectPaths    = 1000
)

func (h Handler) Handle(ctx context.Context, job jobs.Job) error {
	switch job.Kind {
	case jobs.KindSourceParse:
		return h.handleSourceParse(ctx, job)
	case jobs.KindReconcile:
		return h.handleReconciliation(ctx, job)
	case jobs.KindSourceAttachmentCleanup:
		return h.handleSourceAttachmentCleanup(ctx, job)
	default:
		return fmt.Errorf("unsupported transaction processing job kind %q", job.Kind)
	}
}

func (h Handler) handleSourceAttachmentCleanup(ctx context.Context, job jobs.Job) error {
	if h.CleanupAttachments == nil {
		return errors.New("source attachment cleanup handler is not configured")
	}
	if len(job.Payload) == 0 || len(job.Payload) > maxCleanupPayloadBytes {
		return errors.New("source attachment cleanup payload has invalid size")
	}
	var payload jobs.SourceAttachmentCleanupPayload
	decoder := json.NewDecoder(bytes.NewReader(job.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return errors.New("source attachment cleanup payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("source attachment cleanup payload has trailing data")
	}
	sourceID, err := uuid.Parse(payload.SourceID)
	if err != nil || sourceID == uuid.Nil || len(payload.ObjectPaths) == 0 || len(payload.ObjectPaths) > maxCleanupObjectPaths {
		return errors.New("source attachment cleanup payload is invalid")
	}
	seen := make(map[string]struct{}, len(payload.ObjectPaths))
	requests := make([]attachmentstorage.ObjectRequest, 0, len(payload.ObjectPaths))
	for _, objectPath := range payload.ObjectPaths {
		if _, duplicate := seen[objectPath]; duplicate {
			return errors.New("source attachment cleanup payload contains a duplicate path")
		}
		request := attachmentstorage.ObjectRequest{UserID: job.UserID, SourceID: sourceID, ObjectPath: objectPath}
		if err := attachmentstorage.ValidateObjectRequest(request); err != nil {
			return fmt.Errorf("validate source attachment cleanup path: %w", err)
		}
		seen[objectPath] = struct{}{}
		requests = append(requests, request)
	}
	if err := h.CleanupAttachments.Delete(ctx, requests); err != nil {
		return fmt.Errorf("delete source attachments: %w", err)
	}
	return nil
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
	selection, configurationErr := transactionprompt.SelectAutomatic(input)
	encodedComponents, _ := json.Marshal(selection.Components)
	audit := transactionstore.SourceParseAudit{
		SourceID: sourceID, Model: "qwen3.8-flash", AssembledSystemPrompt: selection.AssembledSystemPrompt,
		NormalizedInput: input.NormalizedContent, PromptComponents: encodedComponents,
		RuleID: ruleID(selection.GlobalRule, selection.HasGlobalRule), RuleVersion: ruleVersion(selection.GlobalRule, selection.HasGlobalRule),
		UserRuleID: userRuleID(selection.UserRule, selection.HasUserRule), UserRuleVersion: userRuleVersion(selection.UserRule, selection.HasUserRule),
	}
	if configurationErr != nil {
		if recordErr := h.Repository.RecordFailedSourceParse(ctx, job.UserID, audit, configurationErr); recordErr != nil {
			return fmt.Errorf("record parser configuration failure: %w", recordErr)
		}
		// Retrying cannot help until the conflicting settings are changed.
		return nil
	}
	attachments, usage, err := h.loadParseAttachments(ctx, job.UserID, sourceID, input.Attachments)
	if err != nil {
		audit.AttachmentUsage = usage
		if recordErr := h.Repository.RecordFailedSourceParse(ctx, job.UserID, audit, err); recordErr != nil {
			return fmt.Errorf("record attachment loading failure: %w", recordErr)
		}
		return fmt.Errorf("load parse attachments: %w", err)
	}
	audit.AttachmentUsage = usage
	modelResult, err := h.Parser.ParseTransactionEvidence(ctx, selection.AssembledSystemPrompt, input.NormalizedContent, attachments)
	if modelResult.Model != "" {
		audit.Model = modelResult.Model
	}
	audit.ProviderRequest = jsonObjectAudit(modelResult.ProviderRequest, "raw_request")
	audit.ProviderResponse = jsonObjectAudit(modelResult.ProviderResponse, "raw_response")
	audit.ModelOutput = jsonObjectAudit(modelResult.JSON, "raw_model_output")
	if err != nil {
		if recordErr := h.Repository.RecordFailedSourceParse(ctx, job.UserID, audit, err); recordErr != nil {
			return fmt.Errorf("record parser failure: %w", recordErr)
		}
		return fmt.Errorf("parse source: %w", err)
	}
	parsed, err := reconciliation.DecodeParsedResponseForRuleApplication(modelResult.JSON)
	if err == nil {
		reconciliation.DiscardInvalidOptionalCategoryCitation(&parsed)
		// Model citations can only point at source content; rule: paths are
		// introduced below by trusted server code.
		err = reconciliation.ValidateEvidenceEntries(parsed)
	}
	if err == nil {
		// Ownership is a property of the leased server-side job, never a field
		// requested from or accepted from the model response.
		parsed.Candidate.UserID = job.UserID.String()
		parsed.Candidate.Confidence = reconciliation.AggregateConfidence(parsed.Evidence)
		if selection.HasGlobalRule {
			err = applyDeterministicRule(&parsed.Candidate, &parsed.Evidence, selection.GlobalRule)
		}
		if err == nil {
			parsed.Candidate.AccountEvidence = reconciliation.SanitizeAccountEvidenceForMatching(parsed.Candidate.AccountEvidence, input.NormalizedContent)
			parsed.Candidate.AutoEligible = reconciliation.DeriveAutoEligibility(parsed.Candidate, input.NormalizedContent)
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
		if recordErr := h.Repository.RecordInvalidSourceParse(ctx, job.UserID, audit, err); recordErr != nil {
			return fmt.Errorf("record invalid parser result: %w", recordErr)
		}
		return nil
	}
	if err := h.Repository.SaveParsedSource(ctx, job.UserID, transactionstore.ParsedSourceResult{
		SourceParseAudit: audit, SyncRunID: job.SyncRunID,
		ParsedResponse: parsed, ParsedCandidate: canonicalParsedJSON(parsed),
		AutoEligible: parsed.Candidate.AutoEligible,
	}); err != nil {
		return fmt.Errorf("persist parsed source: %w", err)
	}
	return nil
}

func jsonObjectAudit(raw []byte, fallbackField string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		return append(json.RawMessage(nil), raw...)
	}
	encoded, _ := json.Marshal(map[string]string{fallbackField: string(raw)})
	return encoded
}

func (h Handler) loadParseAttachments(ctx context.Context, userID, _ uuid.UUID, metadata []transactionstore.SourceAttachment) ([]providers.AttachmentInput, []transactionstore.AttachmentUsage, error) {
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
		if !transactionprompt.VisualAttachmentMetadataEligible(item) {
			continue
		}
		request, err := attachmentstorage.ObjectRequestFromPath(userID, item.ObjectPath)
		if err != nil {
			return nil, usage, err
		}
		content, err := h.Attachments.Download(ctx, request)
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

func userRuleID(rule parserrules.UserRule, present bool) string {
	if !present {
		return ""
	}
	return rule.ID
}

func userRuleVersion(rule parserrules.UserRule, present bool) int {
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
