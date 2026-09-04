package bulkimport

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkprompt"
)

const defaultReservationLifetime = 15 * time.Minute

var checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Service struct {
	Store               Store
	Storage             Storage
	Now                 func() time.Time
	ReservationLifetime time.Duration
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) reservationLifetime() time.Duration {
	if s.ReservationLifetime > 0 && s.ReservationLifetime <= time.Hour {
		return s.ReservationLifetime
	}
	return defaultReservationLifetime
}

func (s Service) ListTemplates(ctx context.Context, userID uuid.UUID, includeArchived bool) ([]Template, error) {
	if s.Store == nil || userID == uuid.Nil {
		return nil, ErrInvalid
	}
	return s.Store.ListTemplates(ctx, userID, includeArchived)
}

func (s Service) CreateTemplate(ctx context.Context, userID uuid.UUID, input TemplateInput) (Template, error) {
	if s.Store == nil || userID == uuid.Nil {
		return Template{}, ErrInvalid
	}
	input, err := validateTemplateInput(input, false)
	if err != nil {
		return Template{}, err
	}
	return s.Store.CreateTemplate(ctx, userID, input)
}

func (s Service) UpdateTemplate(ctx context.Context, userID, templateID uuid.UUID, input TemplateInput) (Template, error) {
	if s.Store == nil || userID == uuid.Nil || templateID == uuid.Nil {
		return Template{}, ErrInvalid
	}
	input, err := validateTemplateInput(input, true)
	if err != nil {
		return Template{}, err
	}
	return s.Store.UpdateTemplate(ctx, userID, templateID, input)
}

func (s Service) SetTemplateArchived(ctx context.Context, userID, templateID uuid.UUID, archived bool) (Template, error) {
	if s.Store == nil || userID == uuid.Nil || templateID == uuid.Nil {
		return Template{}, ErrInvalid
	}
	return s.Store.SetTemplateArchived(ctx, userID, templateID, archived)
}

func validateTemplateInput(input TemplateInput, requireVersion bool) (TemplateInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.ParsingPrompt = strings.TrimSpace(input.ParsingPrompt)
	if utf8.RuneCountInString(input.Title) < 1 || utf8.RuneCountInString(input.Title) > 100 ||
		utf8.RuneCountInString(input.ParsingPrompt) < 1 || utf8.RuneCountInString(input.ParsingPrompt) > MaxPromptRunes ||
		!input.DocumentType.Valid() {
		return TemplateInput{}, ErrInvalid
	}
	if requireVersion && (input.ExpectedVersion == nil || *input.ExpectedVersion < 1) {
		return TemplateInput{}, ErrInvalid
	}
	input.AccountIDs = uniqueUUIDs(input.AccountIDs)
	if len(input.AccountIDs) == 0 || (input.DocumentType == DocumentCreditCardBill && len(input.AccountIDs) != 1) {
		return TemplateInput{}, ErrInvalid
	}
	return input, nil
}

func (s Service) CreateBatch(ctx context.Context, userID, templateID uuid.UUID, accountOverride []uuid.UUID) (Batch, error) {
	if s.Store == nil || userID == uuid.Nil || templateID == uuid.Nil {
		return Batch{}, ErrInvalid
	}
	accountOverride = uniqueUUIDs(accountOverride)
	return s.Store.CreateBatch(ctx, userID, templateID, accountOverride)
}

func (s Service) ListBatches(ctx context.Context, userID uuid.UUID, cursor *BatchCursor, limit int) (BatchPage, error) {
	if s.Store == nil || userID == uuid.Nil || limit < 1 || limit > 100 {
		return BatchPage{}, ErrInvalid
	}
	return s.Store.ListBatches(ctx, userID, cursor, limit)
}

func (s Service) GetBatch(ctx context.Context, userID, batchID uuid.UUID) (Batch, error) {
	if s.Store == nil || userID == uuid.Nil || batchID == uuid.Nil {
		return Batch{}, ErrInvalid
	}
	return s.Store.GetBatch(ctx, userID, batchID)
}

// ReserveFile commits a random path before requesting a provider token. A
// signing failure marks the reservation failed in a separate short operation.
func (s Service) ReserveFile(ctx context.Context, userID, batchID uuid.UUID, input ReservationInput) (Reservation, error) {
	if s.Store == nil || s.Storage == nil || userID == uuid.Nil || batchID == uuid.Nil {
		return Reservation{}, ErrInvalid
	}
	clean, extension, err := validateReservation(input)
	if err != nil {
		return Reservation{}, err
	}
	expiresAt := s.now().Add(s.reservationLifetime())
	reserved, err := s.Store.ReserveFile(ctx, userID, batchID, clean, expiresAt)
	if err != nil {
		return Reservation{}, err
	}
	file := reserved.File
	if file.DocumentID == uuid.Nil || file.ID == uuid.Nil || reserved.SourceScopeID == uuid.Nil {
		_ = s.Store.MarkReservationFailed(ctx, userID, file.ID, "reservation returned no document scope")
		return Reservation{}, ErrConflict
	}
	expectedPath := strings.Join([]string{userID.String(), reserved.SourceScopeID.String(), file.ID.String() + "." + extension}, "/")
	if reserved.ObjectPath != expectedPath {
		_ = s.Store.MarkReservationFailed(ctx, userID, file.ID, "reservation path was invalid")
		return Reservation{}, ErrConflict
	}
	uploadURL, err := s.Storage.CreateSignedUpload(ctx, userID, reserved.SourceScopeID, reserved.ObjectPath, s.reservationLifetime())
	if err != nil {
		_ = s.Store.MarkReservationFailed(ctx, userID, file.ID, "signed upload could not be created")
		return Reservation{}, fmt.Errorf("create signed upload: %w", err)
	}
	return Reservation{File: file, UploadURL: uploadURL, Method: "PUT", Headers: map[string]string{"Content-Type": clean.MIMEType, "x-upsert": "false"}}, nil
}

func validateReservation(input ReservationInput) (ReservationInput, string, error) {
	input.DisplayFilename = strings.TrimSpace(filepath.Base(strings.ReplaceAll(input.DisplayFilename, "\\", "/")))
	mediaType, _, err := mime.ParseMediaType(input.MIMEType)
	if err != nil {
		return ReservationInput{}, "", ErrInvalid
	}
	input.MIMEType = strings.ToLower(mediaType)
	extension, ok := bulkExtension(input.MIMEType)
	if !ok || input.ByteSize < 1 || input.ByteSize > MaxFileBytes || !checksumPattern.MatchString(input.SHA256) ||
		input.DisplayFilename == "." || utf8.RuneCountInString(input.DisplayFilename) > 255 {
		return ReservationInput{}, "", ErrInvalid
	}
	return input, extension, nil
}

func bulkExtension(mimeType string) (string, bool) {
	switch mimeType {
	case "application/pdf":
		return "pdf", true
	case "image/bmp":
		return "bmp", true
	case "image/jpeg":
		return "jpg", true
	case "image/png":
		return "png", true
	case "image/tiff":
		return "tiff", true
	case "image/webp":
		return "webp", true
	case "image/heic":
		return "heic", true
	default:
		return "", false
	}
}

func (s Service) FinalizeFile(ctx context.Context, userID, batchID, fileID uuid.UUID) (File, error) {
	if s.Store == nil || s.Storage == nil || userID == uuid.Nil || batchID == uuid.Nil || fileID == uuid.Nil {
		return File{}, ErrInvalid
	}
	batch, err := s.Store.GetBatch(ctx, userID, batchID)
	if err != nil {
		return File{}, err
	}
	var file *File
	var scope uuid.UUID
	for i := range batch.Documents {
		for j := range batch.Documents[i].Files {
			if batch.Documents[i].Files[j].ID == fileID {
				copy := batch.Documents[i].Files[j]
				file, scope = &copy, batch.Documents[i].SourceScopeID
			}
		}
	}
	if file == nil || scope == uuid.Nil {
		return File{}, ErrNotFound
	}
	extension, ok := bulkExtension(file.DeclaredMIME)
	if !ok {
		return File{}, ErrInvalid
	}
	objectPath := strings.Join([]string{userID.String(), scope.String(), file.ID.String() + "." + extension}, "/")
	metadata, err := s.Storage.Stat(ctx, userID, scope, objectPath)
	if err != nil {
		return File{}, fmt.Errorf("inspect finalized upload: %w", err)
	}
	if metadata.ByteSize != file.DeclaredBytes || !strings.EqualFold(strings.TrimSpace(strings.Split(metadata.ContentType, ";")[0]), file.DeclaredMIME) {
		return File{}, ErrConflict
	}
	return s.Store.FinalizeFile(ctx, userID, fileID, metadata)
}

func (s Service) ReplaceDocumentLayout(ctx context.Context, userID, batchID uuid.UUID, layout []DocumentLayout) (Batch, error) {
	if s.Store == nil || userID == uuid.Nil || batchID == uuid.Nil || len(layout) == 0 || len(layout) > MaxFilesPerBatch {
		return Batch{}, ErrInvalid
	}
	seenFiles := map[uuid.UUID]struct{}{}
	for i := range layout {
		layout[i].Label = strings.TrimSpace(layout[i].Label)
		if layout[i].DocumentID == uuid.Nil || len(layout[i].FileIDs) == 0 || len(layout[i].FileIDs) > MaxPages {
			return Batch{}, ErrInvalid
		}
		for _, fileID := range layout[i].FileIDs {
			if fileID == uuid.Nil {
				return Batch{}, ErrInvalid
			}
			if _, duplicate := seenFiles[fileID]; duplicate {
				return Batch{}, ErrInvalid
			}
			seenFiles[fileID] = struct{}{}
		}
	}
	return s.Store.ReplaceDocumentLayout(ctx, userID, batchID, layout)
}

func (s Service) SubmitBatch(ctx context.Context, userID, batchID uuid.UUID) (Batch, error) {
	if s.Store == nil || userID == uuid.Nil || batchID == uuid.Nil {
		return Batch{}, ErrInvalid
	}
	return s.Store.SubmitBatch(ctx, userID, batchID)
}

func (s Service) CancelBatch(ctx context.Context, userID, batchID uuid.UUID) (Batch, error) {
	if s.Store == nil || userID == uuid.Nil || batchID == uuid.Nil {
		return Batch{}, ErrInvalid
	}
	return s.Store.CancelBatch(ctx, userID, batchID)
}

func (s Service) RetryDocument(ctx context.Context, userID, documentID uuid.UUID) (Document, error) {
	if s.Store == nil || userID == uuid.Nil || documentID == uuid.Nil {
		return Document{}, ErrInvalid
	}
	return s.Store.RetryDocument(ctx, userID, documentID)
}

func (s Service) DeleteDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	if s.Store == nil || userID == uuid.Nil || documentID == uuid.Nil {
		return ErrInvalid
	}
	return s.Store.DeleteDocument(ctx, userID, documentID)
}

func (s Service) DeleteBatch(ctx context.Context, userID, batchID uuid.UUID) error {
	if s.Store == nil || userID == uuid.Nil || batchID == uuid.Nil {
		return ErrInvalid
	}
	return s.Store.DeleteBatch(ctx, userID, batchID)
}

func (s Service) ListCandidates(ctx context.Context, userID, batchID uuid.UUID) ([]Candidate, error) {
	if s.Store == nil || userID == uuid.Nil || batchID == uuid.Nil {
		return nil, ErrInvalid
	}
	return s.Store.ListCandidates(ctx, userID, batchID)
}

func (s Service) PreviewPrompt(ctx context.Context, userID, templateID uuid.UUID, accountOverride []uuid.UUID) (PromptPreview, error) {
	if s.Store == nil || userID == uuid.Nil || templateID == uuid.Nil {
		return PromptPreview{}, ErrInvalid
	}
	template, accounts, err := s.Store.LoadPromptPreview(ctx, userID, templateID, uniqueUUIDs(accountOverride))
	if err != nil {
		return PromptPreview{}, err
	}
	descriptors := make([]bulkprompt.AccountDescriptor, 0, len(accounts))
	for _, account := range accounts {
		descriptors = append(descriptors, bulkprompt.AccountDescriptor{AccountRef: account.AccountRef, Name: account.Name, Institution: account.InstitutionName, AccountType: account.AccountType})
	}
	assembly, err := bulkprompt.Assemble(bulkprompt.Input{
		DocumentType: string(template.DocumentType), ChunkIndex: 0,
		PageManifest: []string{"file[0].page[1]"}, TemplatePrompt: template.ParsingPrompt, Accounts: descriptors,
	})
	if err != nil {
		return PromptPreview{}, err
	}
	request, err := json.Marshal(map[string]any{"system": assembly.SystemPrompt, "user": json.RawMessage(assembly.UserMessage), "visuals": assembly.VisualPlaceholders})
	if err != nil {
		return PromptPreview{}, err
	}
	return PromptPreview{SystemPrompt: assembly.SystemPrompt, Request: request}, nil
}

func (s Service) ListDebugAttempts(ctx context.Context, userID, sourceID uuid.UUID) ([]DebugAttempt, error) {
	if s.Store == nil || userID == uuid.Nil || sourceID == uuid.Nil {
		return nil, ErrInvalid
	}
	return s.Store.ListDebugAttempts(ctx, userID, sourceID)
}

func (s Service) GetDebugAttemptField(ctx context.Context, userID, sourceID, attemptID uuid.UUID, field string) (DebugField, error) {
	if s.Store == nil || userID == uuid.Nil || sourceID == uuid.Nil || attemptID == uuid.Nil || strings.TrimSpace(field) == "" {
		return DebugField{}, ErrInvalid
	}
	return s.Store.GetDebugAttemptField(ctx, userID, sourceID, attemptID, field)
}

func (s Service) GetDocumentEvidence(ctx context.Context, userID, documentID uuid.UUID) ([]EvidenceFile, error) {
	if s.Store == nil || s.Storage == nil || userID == uuid.Nil || documentID == uuid.Nil {
		return nil, ErrInvalid
	}
	objects, err := s.Store.LoadDocumentEvidence(ctx, userID, documentID)
	if err != nil {
		return nil, err
	}
	result := make([]EvidenceFile, 0, len(objects))
	for _, object := range objects {
		scopeID, scopeErr := attachmentstorage.ScopeIDFromObjectPath(userID, object.ObjectPath)
		if scopeErr != nil {
			return nil, fmt.Errorf("validate bulk evidence scope: %w", scopeErr)
		}
		signedURL, signErr := s.Storage.SignURL(ctx, userID, scopeID, object.ObjectPath, 300)
		if signErr != nil {
			return nil, fmt.Errorf("sign bulk evidence: %w", signErr)
		}
		result = append(result, EvidenceFile{ID: object.ID, Filename: object.Filename, MIMEType: object.MIMEType, ByteSize: object.ByteSize, SHA256: object.SHA256, SignedURL: signedURL})
	}
	return result, nil
}

func (s Service) ResolveCandidate(ctx context.Context, userID, candidateID uuid.UUID, resolution CandidateResolution) (Candidate, error) {
	if s.Store == nil || userID == uuid.Nil || candidateID == uuid.Nil || resolution.ExpectedGeneration < 1 {
		return Candidate{}, ErrInvalid
	}
	switch resolution.Action {
	case CandidateSetAccount:
		if resolution.AccountID == nil {
			return Candidate{}, ErrInvalid
		}
	case CandidateAttach:
		if resolution.TransactionID == nil {
			return Candidate{}, ErrInvalid
		}
	case CandidateCreate:
		// Account is loaded from the reviewed candidate; only an optional
		// category may be supplied by the user.
	case CandidateInternalTransfer:
		if resolution.DebitAccountID == nil || resolution.CreditAccountID == nil || *resolution.DebitAccountID == *resolution.CreditAccountID {
			return Candidate{}, ErrInvalid
		}
	default:
		return Candidate{}, ErrInvalid
	}
	return s.Store.ResolveCandidate(ctx, userID, candidateID, resolution)
}

func uniqueUUIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func decodeChecksum(value string) ([]byte, error) {
	if !checksumPattern.MatchString(value) {
		return nil, ErrInvalid
	}
	return hex.DecodeString(value)
}
