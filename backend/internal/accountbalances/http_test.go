package accountbalances

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
)

type staticVerifier struct{ user auth.User }

func (v staticVerifier) Verify(context.Context, string) (auth.User, error) { return v.user, nil }

func TestHTTPPreservesMoneyStringsAndExplicitZero(t *testing.T) {
	userID, accountID := uuid.New(), uuid.New()
	asOf := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	repository := &fakeRepository{accounts: []FinancialAccount{{ID: accountID, Name: "Cash", AccountType: "bank_account", Side: AccountAsset, Baseline: []BalanceAmount{{Currency: "SGD", MinorUnits: 0}}, BaselineAsOf: &asOf, BaselineVersion: 1}}}
	mux := http.NewServeMux()
	NewHandler(NewService(repository, nil)).Register(mux, staticVerifier{user: auth.User{ID: userID}})
	request := httptest.NewRequest(http.MethodGet, "/v1/accounts/balances", nil)
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"minor_units":"0"`) || !strings.Contains(recorder.Body.String(), `"state":"configured"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOpeningBalanceHTTPRejectsNumericMoneyAndReturnsConflictETag(t *testing.T) {
	userID, accountID := uuid.New(), uuid.New()
	asOf := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	repository := &fakeRepository{accounts: []FinancialAccount{{ID: accountID, Name: "Cash", AccountType: "bank_account", Side: AccountAsset, BaselineAsOf: &asOf, BaselineVersion: 2}}}
	mux := http.NewServeMux()
	NewHandler(NewService(repository, nil)).Register(mux, staticVerifier{user: auth.User{ID: userID}})

	request := httptest.NewRequest(http.MethodPut, "/v1/accounts/"+accountID.String()+"/opening-balance", strings.NewReader(`{"balances":{"SGD":100},"as_of":"2026-09-01T00:00:00Z","expected_version":2,"correction_reason":"fix"}`))
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("numeric money status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPut, "/v1/accounts/"+accountID.String()+"/opening-balance", strings.NewReader(`{"balances":{"SGD":"100"},"as_of":"2026-09-01T00:00:00Z","expected_version":1,"correction_reason":"fix"}`))
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set("Idempotency-Key", "0123456789abcdef0123456789abcdef")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || recorder.Header().Get("ETag") != `"v-2"` {
		t.Fatalf("conflict status=%d ETag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}
}

func TestCalculationTreatmentGETReturnsDefaultAndPreciseETag(t *testing.T) {
	userID, transactionID := uuid.New(), uuid.New()
	repository := &fakeRepository{transaction: TransactionForTreatment{ID: transactionID}}
	mux := http.NewServeMux()
	NewHandler(NewService(repository, nil)).Register(mux, staticVerifier{user: auth.User{ID: userID}})

	request := httptest.NewRequest(http.MethodGet, "/v1/transaction-calculation-treatments/"+transactionID.String(), nil)
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"t-0"` || !strings.Contains(recorder.Body.String(), `"source":"default"`) {
		t.Fatalf("default status=%d ETag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}

	updatedAt := time.Date(2026, 9, 4, 1, 2, 3, 456000000, time.UTC)
	repository.transaction.Treatment = &CalculationTreatment{TransactionID: transactionID, Basis: SpendingExclude, Source: TreatmentSystem, Reason: systemPayoffReason, UpdatedAt: updatedAt}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != treatmentETag(updatedAt) || !strings.Contains(recorder.Body.String(), `"immutable":true`) {
		t.Fatalf("system status=%d ETag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}
}

func TestOpeningBalanceHTTPRequiresIdempotencyKey(t *testing.T) {
	userID, accountID := uuid.New(), uuid.New()
	repository := &fakeRepository{accounts: []FinancialAccount{{ID: accountID, AccountType: "bank_account", Side: AccountAsset}}}
	mux := http.NewServeMux()
	NewHandler(NewService(repository, nil)).Register(mux, staticVerifier{user: auth.User{ID: userID}})
	request := httptest.NewRequest(http.MethodPut, "/v1/accounts/"+accountID.String()+"/opening-balance", strings.NewReader(`{"balances":{"SGD":"0"},"as_of":"2026-09-01T00:00:00Z","expected_version":0,"correction_reason":null}`))
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
