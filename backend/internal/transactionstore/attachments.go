package transactionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FindIngestedSourceID returns a source already saved by an earlier Gmail job
// attempt so attachment uploads can be retried at their deterministic paths.
func (s *Store) FindIngestedSourceID(ctx context.Context, userID uuid.UUID, providerMessageID string) (uuid.UUID, error) {
	if userID == uuid.Nil || providerMessageID == "" {
		return uuid.Nil, errors.New("user ID and provider message ID are required")
	}
	var sourceID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		select id from private.data_sources
		where user_id = $1 and source_type = 'gmail_email' and provider = 'gmail' and provider_message_id = $2`, userID, providerMessageID).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrSourceNotFound
	}
	return sourceID, err
}

// UpdateIngestedSourceRawData replaces the JSON metadata only after the
// corresponding attachment has been accepted by private Storage. The caller
// must never include attachment bytes or credentials in rawData.
func (s *Store) UpdateIngestedSourceRawData(ctx context.Context, userID, sourceID uuid.UUID, rawData json.RawMessage) error {
	if userID == uuid.Nil || sourceID == uuid.Nil || !json.Valid(rawData) {
		return errors.New("valid user ID, source ID, and raw source metadata are required")
	}
	command, err := s.pool.Exec(ctx, `
		update private.data_sources set raw_data = $3::jsonb
		where id = $1 and user_id = $2 and source_type = 'gmail_email' and provider = 'gmail'`, sourceID, userID, rawData)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrSourceNotFound
	}
	return nil
}
