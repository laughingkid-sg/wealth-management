package gmailconnection

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/secret"
)

type repositoryStub struct {
	encrypted []byte
	label     string
}

func (s *repositoryStub) UpsertGmailConnection(_ context.Context, _ uuid.UUID, encrypted []byte, _ json.RawMessage, label string) error {
	s.encrypted = encrypted
	s.label = label
	return nil
}

func TestStoreRefreshTokenEncryptsBeforePersistence(t *testing.T) {
	cipher, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository := &repositoryStub{}
	service, err := New(repository, cipher, "odin-finance")
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	if err := service.StoreRefreshToken(context.Background(), userID, "refresh-secret", json.RawMessage(`{"scope":"gmail.readonly"}`)); err != nil {
		t.Fatal(err)
	}
	if string(repository.encrypted) == "refresh-secret" || repository.label != "odin-finance" {
		t.Fatalf("token was not safely persisted: %#v", repository)
	}
	plain, err := cipher.Decrypt(repository.encrypted, associatedData(userID))
	if err != nil || string(plain) != "refresh-secret" {
		t.Fatalf("stored ciphertext could not be recovered: %q, %v", plain, err)
	}
}
