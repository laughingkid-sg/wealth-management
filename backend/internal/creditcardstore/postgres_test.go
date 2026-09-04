package creditcardstore

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestExactPayoffDiscoveryRequiresActiveBankAccount(t *testing.T) {
	if !strings.Contains(findExactPayoffTransfersQuery, "debit_account.deleted_at is null") {
		t.Fatal("exact payoff discovery can select an archived Bank Account")
	}
}

func TestBillCursorRoundTrip(t *testing.T) {
	want := billCursor{PeriodEnd: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), ID: uuid.New()}
	got, err := decodeCursor(encodeCursor(want))
	if err != nil || !got.PeriodEnd.Equal(want.PeriodEnd) || got.ID != want.ID {
		t.Fatalf("cursor round trip = %#v, %v", got, err)
	}
	if _, err := decodeCursor("not-base64"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestRequestHashRequiresSHA256Hex(t *testing.T) {
	digest := sha256.Sum256([]byte("request"))
	decoded, err := decodeRequestHash(hex.EncodeToString(digest[:]))
	if err != nil || len(decoded) != sha256.Size {
		t.Fatalf("valid digest rejected: %v", err)
	}
	for _, value := range []string{"", "xyz", "00"} {
		if _, err := decodeRequestHash(value); err == nil {
			t.Errorf("invalid digest %q accepted", value)
		}
	}
}

func TestStoredCandidateNormalizesMigrationAndLegacyKeys(t *testing.T) {
	current := storedCandidate{BillLineIndex: 4, BillLineKind: "refund"}
	current.normalizeLegacyKeys()
	if current.LineIndex != 4 || current.LineKind != "refund" {
		t.Fatalf("migration keys not normalized: %#v", current)
	}
	legacy := storedCandidate{LegacyLineIndex: 3, LegacyLineKind: "fee"}
	legacy.normalizeLegacyKeys()
	if legacy.LineIndex != 3 || legacy.LineKind != "fee" {
		t.Fatalf("legacy keys not normalized: %#v", legacy)
	}
}

func TestProjectCandidateStatusSkipsRepeatedPageDuplicatesAndAuditsFailures(t *testing.T) {
	for _, status := range []string{"duplicate", "cancelled", "superseded"} {
		include, failed, err := projectCandidateStatus(status)
		if err != nil || include || failed {
			t.Fatalf("status %q: include=%t failed=%t err=%v", status, include, failed, err)
		}
	}
	include, failed, err := projectCandidateStatus("failed")
	if err != nil || include || !failed {
		t.Fatalf("failed status: include=%t failed=%t err=%v", include, failed, err)
	}
	if include, _, err = projectCandidateStatus("review_required"); err != nil || !include {
		t.Fatalf("review status: include=%t err=%v", include, err)
	}
}

func TestUnresolvedBulkCountIncludesChunksWithoutRepeatingCandidateFailures(t *testing.T) {
	if got := unresolvedBulkCount(2, 1); got != 3 {
		t.Fatalf("unresolved count = %d, want 3", got)
	}
	if got := unresolvedBulkCount(0, 1); got != 1 {
		t.Fatalf("chunk-only unresolved count = %d, want 1", got)
	}
}
