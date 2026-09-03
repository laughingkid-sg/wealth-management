package transactionprompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

func TestSelectAutomaticUsesProductionMatchersAndStableAssemblyOrder(t *testing.T) {
	globalID, userRuleID := uuid.NewString(), uuid.NewString()
	selection, err := SelectAutomatic(transactionstore.SourceParseInput{
		Subject:                    "Your FairPrice Group app receipt",
		Sender:                     "FairPrice <no-reply@receipts.fairprice.com.sg>",
		Content:                    "Mastercard (**** 2562)",
		NormalizedContent:          "subject: Your FairPrice Group app receipt\nsender: FairPrice <no-reply@receipts.fairprice.com.sg>\ntext: Mastercard (**** 2562)",
		DefaultInstructions:        "Prefer source-stated timestamps.",
		DefaultInstructionsVersion: 3,
		Rules: []parserrules.Rule{
			{ID: uuid.NewString(), Name: "Does not match", Version: 1, Priority: 100, ContentMatcher: `DigitalOcean`, ExtractionConfig: json.RawMessage(`{}`)},
			{ID: globalID, Name: "Masked card", Version: 2, Priority: 50, ContentMatcher: `Mastercard \(\*{4} 2562\)`, PromptFragment: "Read the payment method.", ExtractionConfig: json.RawMessage(`{}`)},
		},
		UserRules: []parserrules.UserRule{{
			ID: userRuleID, Name: "FairPrice", Version: 4, Priority: 10,
			SenderMatchType: "domain", SenderMatchValue: "fairprice.com.sg",
			SubjectMatcher: `app receipt`, PromptFragment: "Use receipt line items.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !selection.HasGlobalRule || selection.GlobalRule.ID != globalID ||
		!selection.HasUserRule || selection.UserRule.ID != userRuleID ||
		!selection.IncludesUserDefault {
		t.Fatalf("selection = %#v", selection)
	}
	ordered := []string{
		providers.ParserPlatformPrompt(),
		"GLOBAL SOURCE GUIDANCE:\nRead the payment method.",
		"USER DEFAULT INSTRUCTIONS:\nPrefer source-stated timestamps.",
		"USER SOURCE-RULE GUIDANCE:\nUse receipt line items.",
	}
	previous := -1
	for _, part := range ordered {
		index := strings.Index(selection.AssembledSystemPrompt, part)
		if index <= previous {
			t.Fatalf("prompt assembly order is wrong for %q", part)
		}
		previous = index
	}
}

func TestSelectManualAllowsInactiveRulesWithoutRunningMatchers(t *testing.T) {
	global := &transactionstore.GlobalSourceParserRule{
		ID: uuid.New(), Name: "Inactive global", Version: 5, Priority: 20,
		Active: false, PromptFragment: "Inactive global guidance.",
	}
	user := &transactionstore.UserSourceParserRule{
		ID: uuid.New(), Name: "Inactive user", Version: 7, Priority: 4,
		Active: false, PromptFragment: "Inactive user guidance.",
	}
	selection := SelectManual("Default guidance.", 2, global, user)
	if !selection.HasGlobalRule || !selection.HasUserRule || !selection.IncludesUserDefault {
		t.Fatalf("manual selection = %#v", selection)
	}
	for _, fragment := range []string{"Inactive global guidance.", "Default guidance.", "Inactive user guidance."} {
		if !strings.Contains(selection.AssembledSystemPrompt, fragment) {
			t.Fatalf("manual prompt omitted %q", fragment)
		}
	}
}

func TestVisualAttachmentMetadataEligibilityMatchesWorkerGate(t *testing.T) {
	eligible := transactionstore.SourceAttachment{
		Filename: "Store Receipt.PDF", ObjectPath: "owner/source/receipt.pdf",
		StorageStatus: "stored", ParseEligible: true,
	}
	if !VisualAttachmentMetadataEligible(eligible) || !HasEligibleVisualAttachment([]transactionstore.SourceAttachment{eligible}) {
		t.Fatal("eligible receipt was rejected")
	}
	for _, mutation := range []func(*transactionstore.SourceAttachment){
		func(value *transactionstore.SourceAttachment) { value.Filename = "statement.pdf" },
		func(value *transactionstore.SourceAttachment) { value.ObjectPath = "" },
		func(value *transactionstore.SourceAttachment) { value.StorageStatus = "skipped" },
		func(value *transactionstore.SourceAttachment) { value.ParseEligible = false },
	} {
		value := eligible
		mutation(&value)
		if VisualAttachmentMetadataEligible(value) {
			t.Fatalf("ineligible attachment passed: %#v", value)
		}
	}
}
