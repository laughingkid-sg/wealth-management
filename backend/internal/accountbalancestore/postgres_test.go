package accountbalancestore

import (
	"reflect"
	"testing"

	"github.com/zhengteck/wealth-builder/backend/internal/accountbalances"
)

func TestBalanceProjectionRoundTripIsExactAndSorted(t *testing.T) {
	raw, err := encodeBalanceProjection([]accountbalances.BalanceAmount{{Currency: "USD", MinorUnits: -9223372036854775808}, {Currency: "SGD", MinorUnits: 0}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBalanceProjection([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []accountbalances.BalanceAmount{{Currency: "SGD", MinorUnits: 0}, {Currency: "USD", MinorUnits: -9223372036854775808}}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded = %#v, want %#v", decoded, want)
	}
}

func TestBalanceProjectionRejectsNonIntegerValues(t *testing.T) {
	if _, err := decodeBalanceProjection([]byte(`{"SGD":"1.25"}`)); err == nil {
		t.Fatal("expected non-integer projection to fail")
	}
}
