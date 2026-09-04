package transactionstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
)

func TestPatchTransactionUpdatesMerchantAndOnlyUserNotesDetail(t *testing.T) {
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
	accountID, transactionID := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2), ($3, $4)`,
		userID, "transaction-patch-"+userID.String()+"@example.test",
		otherUserID, "transaction-patch-"+otherUserID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = any($1::uuid[])`, database.UUIDArrayLiteral([]uuid.UUID{userID, otherUserID}))
	}()
	if _, err = pool.Exec(ctx, `
		insert into public.accounts (
			id, user_id, side, account_type, name, institution_name
		) values ($1, $2, 'liability', 'credit_card', 'Rewards Card', 'Bank')`,
		accountID, userID); err != nil {
		t.Fatal(err)
	}
	provenance := `{"references":["invoice-1"],"account_evidence":{"card_last_four":"2562"},"source_debug":{"parse_attempt":"attempt-1"}}`
	if _, err = pool.Exec(ctx, `
		insert into public.transactions (
			id, user_id, account_id, transaction_kind, title,
			original_amount_minor, original_currency, occurred_at,
			details, review_status, creation_method
		) values (
			$1, $2, $3, 'debit', 'Groceries',
			1250, 'SGD', now(), $4::jsonb, 'confirmed', 'automatic_source'
		)`, transactionID, userID, accountID, provenance); err != nil {
		t.Fatal(err)
	}

	merchant, notes := "FairPrice", "Family groceries"
	updated, err := New(pool).PatchTransaction(ctx, userID, transactionID, TransactionPatch{
		MerchantName: OptionalString{Set: true, Value: &merchant},
		UserNotes:    OptionalString{Set: true, Value: &notes},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.MerchantName == nil || *updated.MerchantName != merchant {
		t.Fatalf("updated merchant = %v", updated.MerchantName)
	}
	var storedMerchant, storedNotes *string
	var provenancePreserved, userModified bool
	if err = pool.QueryRow(ctx, `
		select merchant_name, details ->> 'user_notes',
			details - 'user_notes' = $3::jsonb,
			user_modified_at is not null
		from public.transactions
		where id = $1 and user_id = $2`, transactionID, userID, provenance).Scan(
		&storedMerchant, &storedNotes, &provenancePreserved, &userModified,
	); err != nil {
		t.Fatal(err)
	}
	if storedMerchant == nil || *storedMerchant != merchant ||
		storedNotes == nil || *storedNotes != notes || !provenancePreserved || !userModified {
		t.Fatalf(
			"stored merchant=%v notes=%v provenance=%t user_modified=%t",
			storedMerchant, storedNotes, provenancePreserved, userModified,
		)
	}

	attackerMerchant := "Not owned"
	if _, err = New(pool).PatchTransaction(ctx, otherUserID, transactionID, TransactionPatch{
		MerchantName: OptionalString{Set: true, Value: &attackerMerchant},
	}); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("cross-owner patch error = %v", err)
	}

	emptyNotes := ""
	cleared, err := New(pool).PatchTransaction(ctx, userID, transactionID, TransactionPatch{
		MerchantName: OptionalString{Set: true, Value: nil},
		UserNotes:    OptionalString{Set: true, Value: &emptyNotes},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.MerchantName != nil {
		t.Fatalf("cleared merchant = %v", cleared.MerchantName)
	}
	var merchantRemoved, notesRemoved, detailsUnchanged bool
	if err = pool.QueryRow(ctx, `
		select merchant_name is null,
			not (details ? 'user_notes'),
			details = $3::jsonb
		from public.transactions
		where id = $1 and user_id = $2`, transactionID, userID, provenance).Scan(
		&merchantRemoved, &notesRemoved, &detailsUnchanged,
	); err != nil {
		t.Fatal(err)
	}
	if !merchantRemoved || !notesRemoved || !detailsUnchanged {
		t.Fatalf(
			"merchant_removed=%t notes_removed=%t details_unchanged=%t",
			merchantRemoved, notesRemoved, detailsUnchanged,
		)
	}
}
