package transactionstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCancelDrainedBulkDocumentsSQLQualifiesTimestampAgainstUpdateFromRelations(t *testing.T) {
	if !strings.Contains(cancelDrainedBulkDocumentsSQL, "coalesce(d.completed_at,now())") || strings.Contains(cancelDrainedBulkDocumentsSQL, "coalesce(completed_at,now())") {
		t.Fatalf("cancel SQL must qualify completed_at: %s", cancelDrainedBulkDocumentsSQL)
	}
}

func TestCreditCardCandidateDirectionPolicy(t *testing.T) {
	valid := map[string]string{
		"activity": "debit",
		"fee":      "debit",
		"interest": "debit",
		"refund":   "credit",
		"payment":  "credit",
	}
	for lineKind, transactionKind := range valid {
		if !validCreditCardCandidateDirection(lineKind, transactionKind) {
			t.Errorf("valid %s/%s mapping rejected", lineKind, transactionKind)
		}
		other := "debit"
		if transactionKind == other {
			other = "credit"
		}
		if validCreditCardCandidateDirection(lineKind, other) {
			t.Errorf("invalid %s/%s mapping accepted", lineKind, other)
		}
	}
	if validCreditCardCandidateDirection("summary", "debit") {
		t.Fatal("unsupported line kind accepted")
	}
}

func TestGroupedBulkCleanupKeepsEachImmutableStorageScope(t *testing.T) {
	userID, documentScopeID, laterFileScopeID := uuid.New(), uuid.New(), uuid.New()
	paths := make(scopedObjectPaths)
	paths.addOwned(userID, userID.String()+"/"+documentScopeID.String()+"/first.png")
	paths.addOwned(userID, userID.String()+"/"+laterFileScopeID.String()+"/second.png")
	paths.addOwned(userID, uuid.NewString()+"/"+laterFileScopeID.String()+"/foreign.png")
	if len(paths) != 2 || len(paths[documentScopeID]) != 1 || len(paths[laterFileScopeID]) != 1 {
		t.Fatalf("grouped paths = %#v", paths)
	}
}

func TestPersistedCreditCardCandidateUsesPinnedBillKeys(t *testing.T) {
	candidate := persistedBulkCandidate{BillLineKind: "payment"}
	if candidate.creditCardLineKind() != "payment" {
		t.Fatalf("line kind = %q", candidate.creditCardLineKind())
	}
}
