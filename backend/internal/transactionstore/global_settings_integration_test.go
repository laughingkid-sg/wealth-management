package transactionstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
)

func TestGlobalSourceRuleOptimisticUpdateAndPreviewOwnership(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	userID, otherUserID := uuid.New(), uuid.New()
	for _, user := range []struct {
		id    uuid.UUID
		email string
	}{{userID, "global-settings-" + userID.String() + "@example.test"}, {otherUserID, "global-settings-" + otherUserID.String() + "@example.test"}} {
		if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, user.id, user.email); err != nil {
			t.Fatal(err)
		}
	}
	var ruleID uuid.UUID
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if ruleID != uuid.Nil {
			_, _ = pool.Exec(cleanupContext, `delete from private.source_parser_rules where id = $1`, ruleID)
		}
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, otherUserID)
	}()

	store := New(pool)
	created, err := store.CreateGlobalSourceParserRule(ctx, userID, GlobalSourceParserRuleInput{
		Name: "Integration inactive rule", Provider: "gmail", PromptFragment: "Initial guidance.",
		Priority: -12345, Active: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	ruleID = created.ID
	if created.Version != 1 || created.UpdatedByUserID == nil || *created.UpdatedByUserID != userID {
		t.Fatalf("created rule = %#v", created)
	}

	updated, err := store.UpdateGlobalSourceParserRule(ctx, otherUserID, ruleID, GlobalSourceParserRuleInput{
		Name: "Updated integration rule", Provider: "gmail", PromptFragment: "Updated guidance.",
		Priority: -12344, Active: false, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.UpdatedByUserID == nil || *updated.UpdatedByUserID != otherUserID {
		t.Fatalf("updated rule = %#v", updated)
	}
	if _, err = store.UpdateGlobalSourceParserRule(ctx, userID, ruleID, GlobalSourceParserRuleInput{
		Name: "Stale", Provider: "gmail", ExpectedVersion: 1,
	}); !errors.Is(err, ErrGlobalSourceRuleConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	if _, err = store.GetGlobalSourceParserRule(ctx, uuid.New()); !errors.Is(err, ErrGlobalSourceRuleNotFound) {
		t.Fatalf("missing rule error = %v", err)
	}

	ownedSourceID, foreignSourceID := uuid.New(), uuid.New()
	for _, source := range []struct {
		id, owner uuid.UUID
		subject   string
	}{{ownedSourceID, userID, "Owned receipt"}, {foreignSourceID, otherUserID, "Foreign receipt"}} {
		raw, marshalErr := json.Marshal(map[string]string{"subject": source.subject, "sender": "store@example.test", "text": "SGD 1.00"})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = pool.Exec(ctx, `
			insert into private.data_sources (
				id, user_id, source_type, provider, provider_message_id,
				received_at, raw_data, parse_status
			) values ($1, $2, 'gmail_email', 'gmail', $3, now(), $4::jsonb, 'pending')`,
			source.id, source.owner, "prompt-preview-"+source.id.String(), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := store.ListPromptPreviewSources(ctx, userID, 100)
	if err != nil {
		t.Fatal(err)
	}
	seenOwned, seenForeign := false, false
	for _, source := range sources {
		seenOwned = seenOwned || source.ID == ownedSourceID
		seenForeign = seenForeign || source.ID == foreignSourceID
	}
	if !seenOwned || seenForeign {
		t.Fatalf("preview ownership projection = %#v", sources)
	}
	if _, err = store.LoadSourceParseInput(ctx, userID, foreignSourceID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-owner source load error = %v", err)
	}
}
