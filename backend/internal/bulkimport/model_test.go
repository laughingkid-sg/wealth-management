package bulkimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocumentJSONExposesStoredProgressCounters(t *testing.T) {
	raw, err := json.Marshal(Document{CandidateCount: 8, CreatedCount: 2, AttachedCount: 3, ReviewCount: 1, FailedCount: 1, DuplicateCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"candidate_count":8`, `"created_count":2`, `"attached_count":3`, `"review_count":1`, `"failed_count":1`, `"duplicate_count":1`} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("document JSON %s does not contain %s", raw, expected)
		}
	}
}
