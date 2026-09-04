package creditcard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
)

type staticVerifier struct{ user auth.User }

func (v staticVerifier) Verify(context.Context, string) (auth.User, error) { return v.user, nil }

func creditCardMux(store *fakeStore, userID uuid.UUID) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(NewService(store, nil)).Register(mux, staticVerifier{user: auth.User{ID: userID}})
	return mux
}

func TestBillHTTPReturnsStringMoneyAndETag(t *testing.T) {
	bill := completeReviewBill()
	store := &fakeStore{bill: bill}
	request := httptest.NewRequest(http.MethodGet, "/v1/credit-card-statements/"+bill.ID.String(), nil)
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	creditCardMux(store, uuid.New()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"v-1"` || !strings.Contains(recorder.Body.String(), `"amount_due_minor":"123450"`) {
		t.Fatalf("status=%d ETag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}
}

func TestBillHTTPStrictJSONVersionAndIdempotencyHeaders(t *testing.T) {
	bill := completeReviewBill()
	bill.Status = BillUnpaid
	store := &fakeStore{bill: bill}
	mux := creditCardMux(store, uuid.New())

	request := httptest.NewRequest(http.MethodPatch, "/v1/credit-card-statements/"+bill.ID.String(), strings.NewReader(`{"amount_due_minor":123450,"reason":"confirmed"}`))
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set("If-Match", `"v-1"`)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("numeric money status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/credit-card-statements/"+bill.ID.String()+"/payoff", strings.NewReader(`{"bank_account_id":"`+uuid.NewString()+`"}`))
	request.Header.Set("Authorization", "Bearer test")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/credit-card-statements/"+bill.ID.String()+"/payoff", strings.NewReader(`{"bank_account_id":"`+uuid.NewString()+`","unexpected":true}`))
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set("If-Match", `"v-1"`)
	request.Header.Set("Idempotency-Key", "0123456789abcdef0123456789abcdef")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/credit-card-statements/"+bill.ID.String()+"/payoff", strings.NewReader(`{"bank_account_id":"`+uuid.NewString()+`"}`))
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set("If-Match", `"v-1"`)
	request.Header.Set("Idempotency-Key", "short")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short idempotency key status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
