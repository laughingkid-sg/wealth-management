package bulkimport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type storeStub struct {
	Store
	createdInput    TemplateInput
	reservedInput   ReservationInput
	reserved        ReservedFile
	failedFile      uuid.UUID
	resolution      CandidateResolution
	previewTemplate Template
	previewAccounts []AccountSelection
	evidence        []EvidenceObject
}

func (s *storeStub) CreateTemplate(_ context.Context, _ uuid.UUID, input TemplateInput) (Template, error) {
	s.createdInput = input
	return Template{ID: uuid.New()}, nil
}
func (s *storeStub) ReserveFile(_ context.Context, _ uuid.UUID, _ uuid.UUID, input ReservationInput, _ time.Time) (ReservedFile, error) {
	s.reservedInput = input
	return s.reserved, nil
}
func (s *storeStub) MarkReservationFailed(_ context.Context, _ uuid.UUID, fileID uuid.UUID, _ string) error {
	s.failedFile = fileID
	return nil
}
func (s *storeStub) ResolveCandidate(_ context.Context, _ uuid.UUID, _ uuid.UUID, resolution CandidateResolution) (Candidate, error) {
	s.resolution = resolution
	return Candidate{ID: uuid.New()}, nil
}
func (s *storeStub) LoadPromptPreview(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) (Template, []AccountSelection, error) {
	return s.previewTemplate, s.previewAccounts, nil
}
func (s *storeStub) LoadDocumentEvidence(context.Context, uuid.UUID, uuid.UUID) ([]EvidenceObject, error) {
	return s.evidence, nil
}

type storageStub struct {
	url   string
	err   error
	path  string
	scope uuid.UUID
}

func (s *storageStub) CreateSignedUpload(_ context.Context, _ uuid.UUID, scope uuid.UUID, path string, _ time.Duration) (string, error) {
	s.scope, s.path = scope, path
	return s.url, s.err
}
func (*storageStub) Stat(context.Context, uuid.UUID, uuid.UUID, string) (ObjectMetadata, error) {
	return ObjectMetadata{}, nil
}
func (s *storageStub) SignURL(_ context.Context, _ uuid.UUID, scope uuid.UUID, path string, expires int) (string, error) {
	s.scope, s.path = scope, path
	if expires != 300 {
		return "", errors.New("unexpected expiry")
	}
	return s.url, s.err
}

func TestCreditCardTemplateRequiresExactlyOneAccount(t *testing.T) {
	service := Service{Store: &storeStub{}}
	base := TemplateInput{Title: "Monthly card", DocumentType: DocumentCreditCardBill, ParsingPrompt: "Extract issuer rows."}
	if _, err := service.CreateTemplate(context.Background(), uuid.New(), base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero accounts error=%v", err)
	}
	base.AccountIDs = []uuid.UUID{uuid.New(), uuid.New()}
	if _, err := service.CreateTemplate(context.Background(), uuid.New(), base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("two accounts error=%v", err)
	}
	base.AccountIDs = base.AccountIDs[:1]
	store := &storeStub{}
	service.Store = store
	if _, err := service.CreateTemplate(context.Background(), uuid.New(), base); err != nil {
		t.Fatal(err)
	}
	if len(store.createdInput.AccountIDs) != 1 {
		t.Fatal("validated account not forwarded")
	}
}

func TestReserveFileSignsOnlyTheExactPersistedRandomPath(t *testing.T) {
	userID, batchID, scopeID, fileID, documentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	path := userID.String() + "/" + scopeID.String() + "/" + fileID.String() + ".pdf"
	store := &storeStub{reserved: ReservedFile{File: File{ID: fileID, DocumentID: documentID, DeclaredMIME: "application/pdf"}, SourceScopeID: scopeID, ObjectPath: path}}
	storage := &storageStub{url: "https://project.supabase.co/signed?token=x"}
	result, err := (Service{Store: store, Storage: storage}).ReserveFile(context.Background(), userID, batchID, ReservationInput{DisplayFilename: "statement.pdf", MIMEType: "application/pdf", ByteSize: 10, SHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if storage.path != path || storage.scope != scopeID || result.Headers["x-upsert"] != "false" || result.Method != "PUT" {
		t.Fatalf("result=%#v path=%s", result, storage.path)
	}
}

func TestReserveFileMarksPersistedReservationFailedWhenSigningFails(t *testing.T) {
	userID, scopeID, fileID := uuid.New(), uuid.New(), uuid.New()
	path := userID.String() + "/" + scopeID.String() + "/" + fileID.String() + ".png"
	store := &storeStub{reserved: ReservedFile{File: File{ID: fileID, DocumentID: uuid.New(), DeclaredMIME: "image/png"}, SourceScopeID: scopeID, ObjectPath: path}}
	_, err := (Service{Store: store, Storage: &storageStub{err: errors.New("storage down")}}).ReserveFile(context.Background(), userID, uuid.New(), ReservationInput{DisplayFilename: "page.png", MIMEType: "image/png", ByteSize: 10, SHA256: strings.Repeat("b", 64)})
	if err == nil || store.failedFile != fileID {
		t.Fatalf("err=%v failed=%s", err, store.failedFile)
	}
}

func TestResolveCandidateValidatesActionShape(t *testing.T) {
	store := &storeStub{}
	service := Service{Store: store}
	userID, candidateID := uuid.New(), uuid.New()
	if _, err := service.ResolveCandidate(context.Background(), userID, candidateID, CandidateResolution{Action: CandidateAttach, ExpectedGeneration: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing transaction error=%v", err)
	}
	txID := uuid.New()
	if _, err := service.ResolveCandidate(context.Background(), userID, candidateID, CandidateResolution{Action: CandidateAttach, TransactionID: &txID, ExpectedGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	if store.resolution.TransactionID == nil || *store.resolution.TransactionID != txID {
		t.Fatal("resolution not forwarded")
	}
}

func TestPromptPreviewUsesDescriptorsWithoutDatabaseIDs(t *testing.T) {
	templateID, accountID := uuid.New(), uuid.New()
	store := &storeStub{previewTemplate: Template{ID: templateID, DocumentType: DocumentOther, ParsingPrompt: "Extract rows."}, previewAccounts: []AccountSelection{{AccountID: accountID, AccountRef: "account_1", Name: "Current", InstitutionName: "Bank", AccountType: "bank_account"}}}
	preview, err := (Service{Store: store}).PreviewPrompt(context.Background(), uuid.New(), templateID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(preview.Request), accountID.String()) || !strings.Contains(string(preview.Request), "account_1") {
		t.Fatalf("unsafe preview=%s", preview.Request)
	}
}

func TestDocumentEvidenceSignsOwnedStoredPathsWithoutExposingThem(t *testing.T) {
	userID, documentID, documentScopeID, fileScopeID, fileID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	objectPath := userID.String() + "/" + fileScopeID.String() + "/private.png"
	store := &storeStub{evidence: []EvidenceObject{{ID: fileID, SourceScopeID: documentScopeID, Filename: "statement.png", MIMEType: "image/png", ByteSize: 42, SHA256: strings.Repeat("a", 64), ObjectPath: objectPath}}}
	storage := &storageStub{url: "https://project.supabase.co/storage/signed?token=opaque"}
	items, err := (Service{Store: store, Storage: storage}).GetDocumentEvidence(context.Background(), userID, documentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SignedURL != storage.url || storage.path != objectPath || storage.scope != fileScopeID || strings.Contains(string(mustJSON(t, items)), objectPath) {
		t.Fatalf("unsafe evidence response: %#v", items)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
