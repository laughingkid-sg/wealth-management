package bulkstore

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestScalarUUIDListUsesIndividualTransactionPoolerSafeParameters(t *testing.T) {
	userID := uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	placeholders, arguments := scalarUUIDList(2, []any{userID}, ids)

	if placeholders != "$2,$3,$4" {
		t.Fatalf("placeholders = %q", placeholders)
	}
	if len(arguments) != 4 || arguments[0] != userID {
		t.Fatalf("arguments = %#v", arguments)
	}
	for index, id := range ids {
		if reflect.TypeOf(arguments[index+1]) != reflect.TypeOf(uuid.UUID{}) || arguments[index+1] != id {
			t.Fatalf("argument %d = %#v; want scalar UUID %s", index+1, arguments[index+1], id)
		}
	}
}
