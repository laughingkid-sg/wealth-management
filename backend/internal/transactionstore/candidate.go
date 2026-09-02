package transactionstore

import (
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
