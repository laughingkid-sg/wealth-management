package transactions

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/parserrules"
	"github.com/zhengteck/wealth-builder/backend/internal/providers"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionprompt"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

type globalSourceRuleRequest struct {
	Name            string  `json:"name"`
	Provider        string  `json:"provider"`
	SenderMatcher   *string `json:"sender_matcher"`
	ContentMatcher  *string `json:"content_matcher"`
	PromptFragment  string  `json:"prompt_fragment"`
	Priority        int64   `json:"priority"`
	Active          *bool   `json:"active"`
	ExpectedVersion *int    `json:"expected_version,omitempty"`
}

func (h *Handler) getGlobalSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	setPrivateResponseHeaders(w)
	rules, err := h.repository.ListGlobalSourceParserRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load global transaction settings.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (h *Handler) createGlobalSourceRule(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	setPrivateResponseHeaders(w)
	input, err := decodeGlobalSourceRuleRequest(w, r, false)
	if err != nil {
		return
	}
	rule, err := h.repository.CreateGlobalSourceParserRule(r.Context(), user.ID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create global source rule.")
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) updateGlobalSourceRule(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	setPrivateResponseHeaders(w)
	ruleID, err := uuid.Parse(r.PathValue("id"))
	if err != nil || ruleID == uuid.Nil {
		writeError(w, http.StatusNotFound, "Global source rule not found.")
		return
	}
	input, err := decodeGlobalSourceRuleRequest(w, r, true)
	if err != nil {
		return
	}
	rule, err := h.repository.UpdateGlobalSourceParserRule(r.Context(), user.ID, ruleID, input)
	switch {
	case errors.Is(err, transactionstore.ErrGlobalSourceRuleNotFound):
		writeError(w, http.StatusNotFound, "Global source rule not found.")
		return
	case errors.Is(err, transactionstore.ErrGlobalSourceRuleConflict):
		writeError(w, http.StatusConflict, "This global source rule changed. Reload it before saving again.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "Could not update global source rule.")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func decodeGlobalSourceRuleRequest(w http.ResponseWriter, r *http.Request, requireVersion bool) (transactionstore.GlobalSourceParserRuleInput, error) {
	var request globalSourceRuleRequest
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid global source-rule request.")
		return transactionstore.GlobalSourceParserRuleInput{}, err
	}
	input, err := request.validate(requireVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return transactionstore.GlobalSourceParserRuleInput{}, err
	}
	return input, nil
}

func (request globalSourceRuleRequest) validate(requireVersion bool) (transactionstore.GlobalSourceParserRuleInput, error) {
	name := strings.TrimSpace(request.Name)
	if length := utf8.RuneCountInString(name); length < 1 || length > 100 {
		return transactionstore.GlobalSourceParserRuleInput{}, errors.New("name must be between 1 and 100 characters")
	}
	if request.Provider != "gmail" {
		return transactionstore.GlobalSourceParserRuleInput{}, errors.New("provider must be gmail")
	}
	sender, err := validatedGlobalMatcher(request.SenderMatcher, "sender_matcher", 500)
	if err != nil {
		return transactionstore.GlobalSourceParserRuleInput{}, err
	}
	content, err := validatedGlobalMatcher(request.ContentMatcher, "content_matcher", 1000)
	if err != nil {
		return transactionstore.GlobalSourceParserRuleInput{}, err
	}
	prompt := strings.TrimSpace(request.PromptFragment)
	if utf8.RuneCountInString(prompt) > 4000 {
		return transactionstore.GlobalSourceParserRuleInput{}, errors.New("prompt_fragment must be at most 4000 characters")
	}
	if request.Priority < math.MinInt32 || request.Priority > math.MaxInt32 {
		return transactionstore.GlobalSourceParserRuleInput{}, errors.New("priority is outside the supported range")
	}
	if request.Active == nil {
		return transactionstore.GlobalSourceParserRuleInput{}, errors.New("active is required")
	}
	expectedVersion := 0
	if requireVersion {
		if request.ExpectedVersion == nil || *request.ExpectedVersion < 1 {
			return transactionstore.GlobalSourceParserRuleInput{}, errors.New("expected_version must be a positive integer")
		}
		expectedVersion = *request.ExpectedVersion
	} else if request.ExpectedVersion != nil {
		return transactionstore.GlobalSourceParserRuleInput{}, errors.New("expected_version is only accepted when updating a rule")
	}
	return transactionstore.GlobalSourceParserRuleInput{
		Name: name, Provider: request.Provider, SenderMatcher: sender,
		ContentMatcher: content, PromptFragment: prompt,
		Priority: int(request.Priority), Active: *request.Active,
		ExpectedVersion: expectedVersion,
	}, nil
}

func validatedGlobalMatcher(value *string, field string, maxLength int) (*string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if utf8.RuneCountInString(trimmed) > maxLength {
		return nil, errors.New(field + " must be at most " + jsonNumber(maxLength) + " characters")
	}
	if parserrules.ValidateRE2(trimmed) != nil {
		return nil, errors.New(field + " must be a valid RE2 expression")
	}
	return &trimmed, nil
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (h *Handler) listPromptPreviewSources(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	setPrivateResponseHeaders(w)
	sources, err := h.repository.ListPromptPreviewSources(r.Context(), user.ID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load prompt preview sources.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

type promptPreviewRequest struct {
	Mode               string  `json:"mode"`
	DataSourceID       *string `json:"data_source_id,omitempty"`
	GlobalRuleID       *string `json:"global_rule_id,omitempty"`
	IncludeUserDefault *bool   `json:"include_user_default,omitempty"`
	UserRuleID         *string `json:"user_rule_id,omitempty"`
}

type promptPreviewSelectionItem struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Version  int    `json:"version"`
	Priority int    `json:"priority,omitempty"`
	Reason   string `json:"reason"`
}

type promptPreviewSelection struct {
	GlobalRule  *promptPreviewSelectionItem `json:"global_rule"`
	UserDefault *promptPreviewSelectionItem `json:"user_default"`
	UserRule    *promptPreviewSelectionItem `json:"user_rule"`
}

type promptPreviewResponse struct {
	Mode                  string                                `json:"mode"`
	AssembledSystemPrompt string                                `json:"assembled_system_prompt"`
	PromptComponents      transactionstore.PromptComponents     `json:"prompt_components"`
	SelectedSource        *transactionstore.PromptPreviewSource `json:"selected_source"`
	Selection             promptPreviewSelection                `json:"selection"`
	ProviderRequest       json.RawMessage                       `json:"provider_request"`
}

func (h *Handler) previewPrompt(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	setPrivateResponseHeaders(w)
	var request promptPreviewRequest
	if err := decodeRequestJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid prompt-preview request.")
		return
	}
	var (
		selection transactionprompt.Selection
		source    *transactionstore.PromptPreviewSource
		hasVisual bool
		reason    string
	)
	switch request.Mode {
	case "automatic":
		if request.DataSourceID == nil || request.GlobalRuleID != nil || request.IncludeUserDefault != nil || request.UserRuleID != nil {
			writeError(w, http.StatusBadRequest, "automatic mode requires only data_source_id")
			return
		}
		sourceID, err := parsePreviewUUID(*request.DataSourceID, "data_source_id")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		input, err := h.repository.LoadSourceParseInput(r.Context(), user.ID, sourceID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Prompt preview source not found.")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load prompt preview source.")
			return
		}
		selection, err = transactionprompt.SelectAutomatic(input)
		if errors.Is(err, parserrules.ErrAmbiguousTopPriority) {
			writeError(w, http.StatusConflict, "Matching prompt rules are ambiguous at the highest priority.")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not assemble prompt preview.")
			return
		}
		source = &transactionstore.PromptPreviewSource{
			ID: input.ID, Subject: input.Subject, Sender: input.Sender,
			ReceivedAt: input.ReceivedAt, ParseStatus: input.ParseStatus,
		}
		hasVisual = transactionprompt.HasEligibleVisualAttachment(input.Attachments)
		reason = "matched_top_priority"
	case "manual":
		if request.DataSourceID != nil || request.IncludeUserDefault == nil {
			writeError(w, http.StatusBadRequest, "manual mode requires include_user_default and does not accept data_source_id")
			return
		}
		var globalRule *transactionstore.GlobalSourceParserRule
		if request.GlobalRuleID != nil {
			ruleID, err := parsePreviewUUID(*request.GlobalRuleID, "global_rule_id")
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			rule, err := h.repository.GetGlobalSourceParserRule(r.Context(), ruleID)
			if errors.Is(err, transactionstore.ErrGlobalSourceRuleNotFound) {
				writeError(w, http.StatusNotFound, "Global source rule not found.")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Could not load global source rule.")
				return
			}
			globalRule = &rule
		}
		var defaultInstructions transactionstore.DefaultParserInstructions
		if *request.IncludeUserDefault {
			var err error
			defaultInstructions, err = h.repository.GetDefaultParserInstructions(r.Context(), user.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Could not load default parser instructions.")
				return
			}
		}
		var userRule *transactionstore.UserSourceParserRule
		if request.UserRuleID != nil {
			ruleID, err := parsePreviewUUID(*request.UserRuleID, "user_rule_id")
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			rule, err := h.repository.GetUserSourceParserRule(r.Context(), user.ID, ruleID)
			if errors.Is(err, transactionstore.ErrUserSourceRuleNotFound) {
				writeError(w, http.StatusNotFound, "User source rule not found.")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Could not load user source rule.")
				return
			}
			userRule = &rule
		}
		selection = transactionprompt.SelectManual(
			defaultInstructions.DefaultInstructions,
			defaultInstructions.DefaultInstructionsVersion,
			globalRule,
			userRule,
		)
		reason = "manual_selection"
	default:
		writeError(w, http.StatusBadRequest, "mode must be manual or automatic")
		return
	}
	requestTemplate, err := providers.BuildTransactionParserRequestTemplate(selection.AssembledSystemPrompt, hasVisual)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not build prompt preview.")
		return
	}
	writeJSON(w, http.StatusOK, promptPreviewResponse{
		Mode: request.Mode, AssembledSystemPrompt: selection.AssembledSystemPrompt,
		PromptComponents: selection.Components, SelectedSource: source,
		Selection:       previewSelection(selection, reason),
		ProviderRequest: requestTemplate,
	})
}

func previewSelection(selection transactionprompt.Selection, reason string) promptPreviewSelection {
	result := promptPreviewSelection{}
	if selection.HasGlobalRule {
		result.GlobalRule = &promptPreviewSelectionItem{
			ID: selection.GlobalRule.ID, Name: selection.GlobalRule.Name,
			Version: selection.GlobalRule.Version, Priority: selection.GlobalRule.Priority,
			Reason: reason,
		}
	}
	if selection.IncludesUserDefault {
		component := selection.Components.UserDefault
		result.UserDefault = &promptPreviewSelectionItem{
			ID: component.ID, Version: component.Version, Reason: reason,
		}
	}
	if selection.HasUserRule {
		result.UserRule = &promptPreviewSelectionItem{
			ID: selection.UserRule.ID, Name: selection.UserRule.Name,
			Version: selection.UserRule.Version, Priority: selection.UserRule.Priority,
			Reason: reason,
		}
	}
	return result
}

func parsePreviewUUID(value, field string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, errors.New(field + " must be a UUID")
	}
	return parsed, nil
}

func setPrivateResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
