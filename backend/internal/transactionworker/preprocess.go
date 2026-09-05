package transactionworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zhengteck/wealth-builder/backend/internal/scriptstore"
	"github.com/zhengteck/wealth-builder/backend/internal/transactionstore"
)

// defaultPreProcessScriptKey is the global script consulted before the LLM when
// a matched rule names no pre-process script (per-rule selection lands later).
const defaultPreProcessScriptKey = "email_pre_process"

// ScriptEngine runs a sandboxed operator script with a strict JSON-in/JSON-out
// contract. It is satisfied by *scriptengine.Engine.
type ScriptEngine interface {
	Run(ctx context.Context, source string, input json.RawMessage) (json.RawMessage, error)
}

// ScriptResolver returns the active version of a script by key. It is satisfied
// by *scriptstore.Store. A missing active version yields ErrNoActiveScript.
type ScriptResolver interface {
	LoadActiveScript(ctx context.Context, key string) (scriptstore.ActiveScript, error)
}

// preprocessInput is the JSON document handed to a pre-process script. It
// carries only the normalized email and attachment metadata — never account
// data or attachment bytes.
type preprocessInput struct {
	Subject           string                 `json:"subject"`
	Sender            string                 `json:"sender"`
	Text              string                 `json:"text"`
	ReceivedAt        string                 `json:"received_at"`
	NormalizedContent string                 `json:"normalized_content"`
	Attachments       []preprocessAttachment `json:"attachments"`
}

type preprocessAttachment struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
}

// preprocessOutput is the strict shape a pre-process script must return: the
// cleaned normalized content the LLM will receive.
type preprocessOutput struct {
	NormalizedContent string `json:"normalized_content"`
}

// preprocessNormalizedContent runs the active pre-process script over the
// normalized email and returns the content to send to the LLM plus an audit
// note. The stage is inert (returns the original content, empty note) when no
// engine/resolver is configured or no active script is seeded. Any error or
// invalid output falls back to the original content so a bad script degrades the
// LLM input rather than blocking ingestion.
func (h Handler) preprocessNormalizedContent(ctx context.Context, input transactionstore.SourceParseInput) (string, string) {
	if h.Engine == nil || h.Scripts == nil {
		return input.NormalizedContent, ""
	}
	script, err := h.Scripts.LoadActiveScript(ctx, defaultPreProcessScriptKey)
	if errors.Is(err, scriptstore.ErrNoActiveScript) {
		return input.NormalizedContent, ""
	}
	if err != nil {
		return input.NormalizedContent, "fallback:load_error"
	}
	attachments := make([]preprocessAttachment, 0, len(input.Attachments))
	for _, a := range input.Attachments {
		attachments = append(attachments, preprocessAttachment{Filename: a.Filename, MIMEType: a.MIMEType})
	}
	payload, err := json.Marshal(preprocessInput{
		Subject: input.Subject, Sender: input.Sender, Text: input.Content,
		ReceivedAt:        input.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		NormalizedContent: input.NormalizedContent, Attachments: attachments,
	})
	if err != nil {
		return input.NormalizedContent, "fallback:marshal_error"
	}
	out, err := h.Engine.Run(ctx, script.Source, payload)
	if err != nil {
		return input.NormalizedContent, "fallback:run_error"
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.DisallowUnknownFields()
	var parsed preprocessOutput
	if decoder.Decode(&parsed) != nil || strings.TrimSpace(parsed.NormalizedContent) == "" {
		return input.NormalizedContent, "fallback:invalid_output"
	}
	return parsed.NormalizedContent, fmt.Sprintf("%s:v%d", script.Key, script.Version)
}
