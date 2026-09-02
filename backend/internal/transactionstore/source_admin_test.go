package transactionstore

import "testing"

func TestSourceParseAuditFieldWhitelistHasExplicitHardBounds(t *testing.T) {
	want := map[string]int{
		"request_metadata":        65536,
		"parsed_candidate":        2097152,
		"assembled_system_prompt": 65536,
		"normalized_input":        262144,
		"provider_request":        10485760,
		"provider_response":       2097152,
		"model_output":            2097152,
		"prompt_components":       65536,
	}
	for field, expectedMax := range want {
		expression, maxBytes, ok := sourceParseAuditFieldSpec(field)
		if !ok || expression == "" || maxBytes != expectedMax {
			t.Fatalf("field %q = expression %q max %d ok %t", field, expression, maxBytes, ok)
		}
	}
	if expression, maxBytes, ok := sourceParseAuditFieldSpec("api_key"); ok || expression != "" || maxBytes != 0 {
		t.Fatalf("unexpected field was accepted: expression %q max %d", expression, maxBytes)
	}
}

func TestDeletedProviderIdentityDigestIsDomainSeparated(t *testing.T) {
	base := sourceProviderIdentityDigest("gmail_email", "gmail", "message-1")
	if base == sourceProviderIdentityDigest("gmail_email", "gmail", "message-2") ||
		base == sourceProviderIdentityDigest("phone_notification", "gmail", "message-1") ||
		base == sourceProviderIdentityDigest("gmail_email", "other", "message-1") {
		t.Fatal("provider identity digest did not distinguish all identity components")
	}
	if base != sourceProviderIdentityDigest("gmail_email", "gmail", "message-1") {
		t.Fatal("provider identity digest is not deterministic")
	}
}
