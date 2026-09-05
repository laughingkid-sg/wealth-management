package transactionstore

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/reconciliation"
)

// decodePersistedCandidate is exclusively for parser results already
// validated and stored by the worker. It accepts trusted deterministic rule
// evidence, then restores the owner and confidence fields that are
// intentionally excluded from JSON.
func decodePersistedCandidate(raw []byte, userID uuid.UUID) (reconciliation.ParsedResponse, error) {
	parsed, err := reconciliation.DecodeParsedResponseForRuleApplication(raw)
	if err != nil {
		return reconciliation.ParsedResponse{}, err
	}
	if err = reconciliation.ValidateParsedResponseAfterRule(parsed); err != nil {
		return reconciliation.ParsedResponse{}, fmt.Errorf("validate persisted parser result: %w", err)
	}
	parsed.Candidate.UserID = userID.String()
	parsed.Candidate.Confidence = reconciliation.AggregateConfidence(parsed.Evidence)
	if err = reconciliation.ValidateCandidate(parsed.Candidate); err != nil {
		return reconciliation.ParsedResponse{}, fmt.Errorf("validate server-bound parser candidate: %w", err)
	}
	return parsed, nil
}

// decodePersistedSourceCandidate decodes a per-candidate row stored by the Gmail
// pipeline (persistedSourceCandidate: parsed response plus server-derived
// confidence and auto-eligibility) and restores the server-owned fields. Unlike
// decodePersistedCandidate it tolerates the confidence/auto_eligible keys.
func decodePersistedSourceCandidate(raw []byte, userID uuid.UUID) (reconciliation.ParsedResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored persistedSourceCandidate
	if err := decoder.Decode(&stored); err != nil {
		return reconciliation.ParsedResponse{}, fmt.Errorf("decode persisted source candidate: %w", err)
	}
	parsed := stored.ParsedResponse
	if err := reconciliation.ValidateParsedResponseAfterRule(parsed); err != nil {
		return reconciliation.ParsedResponse{}, fmt.Errorf("validate persisted source candidate: %w", err)
	}
	parsed.Candidate.UserID = userID.String()
	parsed.Candidate.Confidence = stored.Confidence
	parsed.Candidate.AutoEligible = stored.AutoEligible
	if err := reconciliation.ValidateCandidate(parsed.Candidate); err != nil {
		return reconciliation.ParsedResponse{}, fmt.Errorf("validate server-bound source candidate: %w", err)
	}
	return parsed, nil
}
