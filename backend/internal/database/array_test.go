package database

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestTextArrayLiteralEscapesElements(t *testing.T) {
	got := TextArrayLiteral([]string{"gmail_ingestion", "comma,value", `quote"and\slash`, "NULL"})
	want := `{"gmail_ingestion","comma,value","quote\"and\\slash","NULL"}`
	if got != want {
		t.Fatalf("TextArrayLiteral() = %q, want %q", got, want)
	}
	if got := TextArrayLiteral(nil); got != "{}" {
		t.Fatalf("TextArrayLiteral(nil) = %q, want empty array", got)
	}
}

func TestArrayLiteralsEncodeThroughSimpleProtocolBoundary(t *testing.T) {
	ids := []uuid.UUID{
		uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		uuid.MustParse("22222222-2222-2222-2222-222222222222"),
	}
	literal := UUIDArrayLiteral(ids)
	want := `{"11111111-1111-1111-1111-111111111111","22222222-2222-2222-2222-222222222222"}`
	if literal != want {
		t.Fatalf("UUIDArrayLiteral() = %q, want %q", literal, want)
	}

	// Simple-protocol execution first asks pgtype to encode every argument
	// with unknown OID 0 in text format. A string literal crosses that exact
	// boundary without requiring pgx to infer a slice's PostgreSQL array type.
	encoded, err := pgtype.NewMap().Encode(0, pgtype.TextFormatCode, literal, nil)
	if err != nil {
		t.Fatalf("encode UUID array literal for simple protocol: %v", err)
	}
	if string(encoded) != want {
		t.Fatalf("simple-protocol encoding = %q, want %q", encoded, want)
	}
}

func TestNullableTextArrayLiteralPreservesNil(t *testing.T) {
	if got := NullableTextArrayLiteral(nil); got != nil {
		t.Fatalf("NullableTextArrayLiteral(nil) = %#v, want nil", got)
	}
	if got := NullableTextArrayLiteral([]string{}); got != "{}" {
		t.Fatalf("NullableTextArrayLiteral(empty) = %#v, want empty array", got)
	}
}

func TestArrayLiteralsRoundTripWithTransactionPooler(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_TEST_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ids := []uuid.UUID{uuid.New(), uuid.New()}
	var uuidCount, textCount int
	var nilWasNull bool
	err = pool.QueryRow(ctx, `select cardinality($1::uuid[]), cardinality($2::text[]), $3::text[] is null`,
		UUIDArrayLiteral(ids), TextArrayLiteral([]string{"one", "two"}), NullableTextArrayLiteral(nil),
	).Scan(&uuidCount, &textCount, &nilWasNull)
	if err != nil {
		t.Fatal(err)
	}
	if uuidCount != 2 || textCount != 2 || !nilWasNull {
		t.Fatalf("array round trip = (%d, %d, %v), want (2, 2, true)", uuidCount, textCount, nilWasNull)
	}
}
