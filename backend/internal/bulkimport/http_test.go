package bulkimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
)

type evidenceApplication struct {
	Application
	userID     uuid.UUID
	documentID uuid.UUID
	items      []EvidenceFile
}

func (a *evidenceApplication) GetDocumentEvidence(_ context.Context, userID, documentID uuid.UUID) ([]EvidenceFile, error) {
	a.userID, a.documentID = userID, documentID
	return a.items, nil
}

type evidenceVerifier struct{ user auth.User }

func (v evidenceVerifier) Verify(context.Context, string) (auth.User, error) { return v.user, nil }

type debugApplication struct {
	Application
	userID, sourceID, attemptID uuid.UUID
	field                       string
}

type duplicateReservationApplication struct {
	Application
	input ReservationInput
}

func (a *duplicateReservationApplication) ReserveFile(_ context.Context, _ uuid.UUID, _ uuid.UUID, input ReservationInput) (Reservation, error) {
	a.input = input
	if !input.IntentionalDuplicate {
		return Reservation{}, ErrDuplicateFile
	}
	return Reservation{}, nil
}

func (a *debugApplication) GetDebugAttemptField(_ context.Context, userID, sourceID, attemptID uuid.UUID, field string) (DebugField, error) {
	a.userID, a.sourceID, a.attemptID, a.field = userID, sourceID, attemptID, field
	value := `{"model":"exact"}`
	return DebugField{SourceID: sourceID, AttemptID: attemptID, Field: field, Value: &value, MaxBytes: 2097152}, nil
}

func TestDocumentEvidenceRouteIsAuthenticatedAndReturnsNoPrivatePath(t *testing.T) {
	userID, documentID, fileID := uuid.New(), uuid.New(), uuid.New()
	application := &evidenceApplication{items: []EvidenceFile{{ID: fileID, Filename: "statement.pdf", MIMEType: "application/pdf", ByteSize: 42, SHA256: strings.Repeat("a", 64), SignedURL: "https://storage.example.test/signed?token=opaque"}}}
	mux := http.NewServeMux()
	(Handler{Application: application}).Register(mux, evidenceVerifier{user: auth.User{ID: userID}})

	request := httptest.NewRequest(http.MethodGet, "/v1/bulk-import/documents/"+documentID.String(), nil)
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || application.userID != userID || application.documentID != documentID {
		t.Fatalf("status=%d user=%s document=%s", response.Code, application.userID, application.documentID)
	}
	body := response.Body.String()
	if !strings.Contains(body, "signed?token=opaque") || strings.Contains(body, "object_path") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe evidence response headers=%v body=%s", response.Header(), body)
	}
}

func TestBulkDebugFieldRouteIsOwnerScopedAndNoStore(t *testing.T) {
	userID, sourceID, attemptID := uuid.New(), uuid.New(), uuid.New()
	application := &debugApplication{}
	mux := http.NewServeMux()
	(Handler{Application: application}).Register(mux, evidenceVerifier{user: auth.User{ID: userID}})
	request := httptest.NewRequest(http.MethodGet, "/v1/transactions/sources/"+sourceID.String()+"/debug/bulk-attempts/"+attemptID.String()+"/fields/provider_response", nil)
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || application.userID != userID || application.sourceID != sourceID || application.attemptID != attemptID || application.field != "provider_response" {
		t.Fatalf("status=%d headers=%v application=%#v", response.Code, response.Header(), application)
	}
	if !strings.Contains(response.Body.String(), `\"model\":\"exact\"`) {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestDuplicateChecksumConflictHasMachineReadableCodeAndCanBeOverridden(t *testing.T) {
	userID, batchID := uuid.New(), uuid.New()
	application := &duplicateReservationApplication{}
	mux := http.NewServeMux()
	(Handler{Application: application}).Register(mux, evidenceVerifier{user: auth.User{ID: userID}})

	requestBody := `{"filename":"statement.pdf","mime_type":"application/pdf","byte_size":42,"sha256":"` + strings.Repeat("a", 64) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/transactions/bulk-import/batches/"+batchID.String()+"/files/reservations", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"duplicate_file"`) || !strings.Contains(response.Body.String(), "checksum") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	overrideBody := `{"filename":"statement.pdf","mime_type":"application/pdf","byte_size":42,"sha256":"` + strings.Repeat("a", 64) + `","intentional_duplicate":true}`
	override := httptest.NewRequest(http.MethodPost, "/v1/transactions/bulk-import/batches/"+batchID.String()+"/files/reservations", strings.NewReader(overrideBody))
	override.Header.Set("Authorization", "Bearer test")
	overrideResponse := httptest.NewRecorder()
	mux.ServeHTTP(overrideResponse, override)
	if overrideResponse.Code != http.StatusCreated || !application.input.IntentionalDuplicate {
		t.Fatalf("override status=%d input=%#v body=%s", overrideResponse.Code, application.input, overrideResponse.Body.String())
	}
}

func TestOrdinaryConflictResponseDoesNotMasqueradeAsDuplicate(t *testing.T) {
	response := httptest.NewRecorder()
	writeAPIError(response, ErrConflict)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "duplicate_file") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
