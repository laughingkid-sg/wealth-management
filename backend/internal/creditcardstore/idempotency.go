package creditcardstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/creditcard"
)

const idempotencyResourceType = "credit_card_statement"

func (t *transaction) ClaimIdempotency(ctx context.Context, userID uuid.UUID, key, operation string, resourceID uuid.UUID, requestHash string, expiresAt time.Time) (creditcard.IdempotencyClaim, error) {
	keyDigest := sha256.Sum256([]byte(key))
	hash, err := decodeRequestHash(requestHash)
	if err != nil {
		return creditcard.IdempotencyClaim{}, err
	}
	var recordID uuid.UUID
	err = t.tx.QueryRow(ctx, `
		insert into private.api_idempotency_records (
			user_id, operation, key_digest, request_hash,
			resource_type, resource_id, status, expires_at
		) values ($1, $2, $3, $4, $5, $6, 'processing', $7)
		on conflict (user_id, operation, key_digest) do nothing
		returning id`, userID, operation, keyDigest[:], hash, idempotencyResourceType, resourceID, expiresAt).Scan(&recordID)
	if err == nil {
		return creditcard.IdempotencyClaim{State: creditcard.IdempotencyAcquired}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return creditcard.IdempotencyClaim{}, err
	}
	var storedHash []byte
	var storedResourceType *string
	var storedResourceID *uuid.UUID
	var status string
	var response []byte
	var responseStatus *int
	var expired bool
	err = t.tx.QueryRow(ctx, `
			select id, request_hash, resource_type, resource_id, status, response_status,
				response_body, expires_at <= now()
		from private.api_idempotency_records
		where user_id = $1 and operation = $2 and key_digest = $3
		for update`, userID, operation, keyDigest[:]).
		Scan(&recordID, &storedHash, &storedResourceType, &storedResourceID, &status, &responseStatus, &response, &expired)
	if err != nil {
		return creditcard.IdempotencyClaim{}, err
	}
	if expired || status == "failed" {
		_, err = t.tx.Exec(ctx, `
			update private.api_idempotency_records set
				request_hash = $2, resource_type = $3, resource_id = $4,
				status = 'processing', response_status = null, response_body = null,
				expires_at = $5
			where id = $1`, recordID, hash, idempotencyResourceType, resourceID, expiresAt)
		if err != nil {
			return creditcard.IdempotencyClaim{}, err
		}
		return creditcard.IdempotencyClaim{State: creditcard.IdempotencyAcquired}, nil
	}
	if !bytes.Equal(storedHash, hash) || storedResourceType == nil || *storedResourceType != idempotencyResourceType || storedResourceID == nil || *storedResourceID != resourceID {
		return creditcard.IdempotencyClaim{}, creditcard.ErrIdempotencyConflict
	}
	switch status {
	case "processing":
		return creditcard.IdempotencyClaim{State: creditcard.IdempotencyBusy}, nil
	case "completed":
		if responseStatus == nil {
			return creditcard.IdempotencyClaim{}, fmt.Errorf("idempotency replay %s has no response status", recordID)
		}
		if *responseStatus == 204 && len(response) == 0 {
			return creditcard.IdempotencyClaim{State: creditcard.IdempotencyReplay, ResponseStatus: *responseStatus}, nil
		}
		var bill creditcard.Bill
		if len(response) == 0 || json.Unmarshal(response, &bill) != nil || bill.ID != resourceID {
			return creditcard.IdempotencyClaim{}, fmt.Errorf("decode idempotency replay for %s", recordID)
		}
		return creditcard.IdempotencyClaim{State: creditcard.IdempotencyReplay, Bill: &bill, ResponseStatus: *responseStatus}, nil
	default:
		return creditcard.IdempotencyClaim{}, fmt.Errorf("unknown idempotency status %q", status)
	}
}

func (t *transaction) CompleteIdempotency(ctx context.Context, userID uuid.UUID, key, operation string, resourceID uuid.UUID, requestHash string, bill *creditcard.Bill, responseStatus int) error {
	keyDigest := sha256.Sum256([]byte(key))
	hash, err := decodeRequestHash(requestHash)
	if err != nil {
		return err
	}
	var response []byte
	if bill != nil {
		response, err = json.Marshal(bill)
		if err != nil {
			return err
		}
	}
	if responseStatus != 200 && responseStatus != 204 || responseStatus == 200 && bill == nil || responseStatus == 204 && bill != nil {
		return fmt.Errorf("%w: invalid idempotency response", creditcard.ErrValidation)
	}
	command, err := t.tx.Exec(ctx, `
			update private.api_idempotency_records set
				status = 'completed', response_status = $7, response_body = $8::jsonb
			where user_id = $1 and operation = $2 and key_digest = $3
				and request_hash = $4 and resource_type = $5 and resource_id = $6
				and status = 'processing'`, userID, operation, keyDigest[:], hash,
		idempotencyResourceType, resourceID, responseStatus, nullableJSON(response))
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return creditcard.ErrIdempotencyConflict
	}
	return nil
}

func nullableJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func decodeRequestHash(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("%w: invalid canonical request hash", creditcard.ErrValidation)
	}
	return decoded, nil
}
