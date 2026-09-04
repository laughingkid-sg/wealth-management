package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhengteck/wealth-builder/backend/internal/accountbalances"
	"github.com/zhengteck/wealth-builder/backend/internal/auth"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
	"github.com/zhengteck/wealth-builder/backend/internal/creditcard"
	"github.com/zhengteck/wealth-builder/backend/internal/transactions"
)

type pathRegistrar string

func (path pathRegistrar) Register(mux *http.ServeMux, _ auth.Verifier) {
	mux.Handle("GET "+string(path), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func TestRegisterFeatureRoutesGatesOnlyBulk(t *testing.T) {
	for _, test := range []struct {
		name        string
		bulkEnabled bool
		bulkStatus  int
	}{
		{name: "disabled", bulkStatus: http.StatusNotFound},
		{name: "enabled", bulkEnabled: true, bulkStatus: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerFeatureRoutes(mux, nil, pathRegistrar("/balances"), pathRegistrar("/credit-card"), pathRegistrar("/bulk"), test.bulkEnabled)
			for path, want := range map[string]int{"/balances": http.StatusNoContent, "/credit-card": http.StatusNoContent, "/bulk": test.bulkStatus} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != want {
					t.Errorf("GET %s = %d, want %d", path, response.Code, want)
				}
			}
		})
	}
}

func TestProductionRouteRegistrationsDoNotConflict(t *testing.T) {
	mux := http.NewServeMux()

	transactions.NewHandler(nil, false, nil, nil).Register(mux, nil)
	registerFeatureRoutes(
		mux,
		nil,
		accountbalances.NewHandler(accountbalances.NewService(nil, nil)),
		creditcard.NewHandler(creditcard.NewService(nil, nil)),
		bulkimport.Handler{},
		true,
	)
}
