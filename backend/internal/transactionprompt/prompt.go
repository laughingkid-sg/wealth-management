// Package transactionprompt selects configured parser instructions and builds
// the exact system prompt shared by production parsing and read-only previews.
package transactionprompt

import (
	"errors"
	"strings"

	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type Selection struct {
	AssembledSystemPrompt string
	Components            transactionstore.PromptComponents
	GlobalRule            parserrules.AppliedRule
	HasGlobalRule         bool
	UserRule              parserrules.UserRule
	HasUserRule           bool
	IncludesUserDefault   bool
}

// SelectAutomatic evaluates the same active global and owner-scoped rules used
// by the worker. The returned selection remains useful for diagnostics if an
// ambiguity error is present.
func SelectAutomatic(input transactionstore.SourceParseInput) (Selection, error) {
	globalRule, hasGlobalRule, globalErr := parserrules.MatchAndApply(input.Sender, input.NormalizedContent, input.Rules)
	userRule, hasUserRule, userErr := parserrules.MatchUserRule(input.Sender, input.Subject, input.Content, input.UserRules)
	selection := build(
		input.DefaultInstructions,
		input.DefaultInstructionsVersion,
		globalRule,
		hasGlobalRule,
		userRule,
		hasUserRule,
	)
	return selection, errors.Join(globalErr, userErr)
}

// SelectManual composes explicitly selected configuration without evaluating
// its matchers. This lets a user inspect inactive rules without changing them.
func SelectManual(defaultInstructions string, defaultVersion int, globalRule *transactionstore.GlobalSourceParserRule, userRule *transactionstore.UserSourceParserRule) Selection {
	var appliedGlobal parserrules.AppliedRule
	hasGlobal := globalRule != nil
	if globalRule != nil {
		appliedGlobal = parserrules.AppliedRule{
			ID:             globalRule.ID.String(),
			Name:           globalRule.Name,
			Version:        globalRule.Version,
			Priority:       globalRule.Priority,
			PromptFragment: globalRule.PromptFragment,
		}
	}
	var selectedUser parserrules.UserRule
	hasUser := userRule != nil
	if userRule != nil {
		selectedUser = parserrules.UserRule{
			ID:             userRule.ID.String(),
			Name:           userRule.Name,
			Version:        userRule.Version,
			Priority:       userRule.Priority,
			PromptFragment: userRule.PromptFragment,
		}
	}
	return build(defaultInstructions, defaultVersion, appliedGlobal, hasGlobal, selectedUser, hasUser)
}

func build(defaultInstructions string, defaultVersion int, global parserrules.AppliedRule, hasGlobal bool, user parserrules.UserRule, hasUser bool) Selection {
	defaultInstructions = strings.TrimSpace(defaultInstructions)
	components := transactionstore.PromptComponents{Platform: transactionstore.PromptComponent{
		ID: "wealth-builder-transaction-parser", Version: providers.ParserPlatformPromptVersion, Content: providers.ParserPlatformPrompt(),
	}}
	if hasGlobal {
		components.GlobalRule = &transactionstore.PromptComponent{
			ID: global.ID, Name: global.Name, Version: global.Version, Content: strings.TrimSpace(global.PromptFragment),
		}
	}
	if defaultInstructions != "" {
		components.UserDefault = &transactionstore.PromptComponent{
			ID: "user-parser-settings", Version: defaultVersion, Content: defaultInstructions,
		}
	}
	if hasUser {
		components.UserSourceRule = &transactionstore.PromptComponent{
			ID: user.ID, Name: user.Name, Version: user.Version, Content: strings.TrimSpace(user.PromptFragment),
		}
	}
	return Selection{
		AssembledSystemPrompt: providers.AssembleParserSystemPrompt(providers.ParserPromptFragments{
			GlobalRule:     global.PromptFragment,
			DefaultUser:    defaultInstructions,
			UserSourceRule: user.PromptFragment,
		}),
		Components:          components,
		GlobalRule:          global,
		HasGlobalRule:       hasGlobal,
		UserRule:            user,
		HasUserRule:         hasUser,
		IncludesUserDefault: defaultInstructions != "",
	}
}

// HasEligibleVisualAttachment mirrors the worker's pre-download metadata gate.
// The preview shows one placeholder when at least one source attachment could
// be sent after download/rendering.
func HasEligibleVisualAttachment(attachments []transactionstore.SourceAttachment) bool {
	for _, attachment := range attachments {
		if VisualAttachmentMetadataEligible(attachment) {
			return true
		}
	}
	return false
}

func VisualAttachmentMetadataEligible(attachment transactionstore.SourceAttachment) bool {
	filename := strings.ToLower(attachment.Filename)
	return attachment.StorageStatus == "stored" &&
		attachment.ParseEligible &&
		strings.TrimSpace(attachment.ObjectPath) != "" &&
		(strings.Contains(filename, "receipt") || strings.Contains(filename, "invoice"))
}
