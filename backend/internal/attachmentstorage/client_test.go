package attachmentstorage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestClientUploadsToPrivateBucketWithDeterministicPath(t *testing.T) {
	userID := uuid.MustParse("51feb44a-7f6a-4964-a66e-3f4ba9b598f1")
	sourceID := uuid.MustParse("d8a3c6c3-0ea1-4d46-a86f-3a4c1882596e")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s", request.Method)
		}
		if request.URL.Path != "/storage/v1/object/transaction-attachments/51feb44a-7f6a-4964-a66e-3f4ba9b598f1/d8a3c6c3-0ea1-4d46-a86f-3a4c1882596e/3c87d37f1dbea6909f917ce437c390fb8e655a774387d9e69301c0b2283d5b63.pdf" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer service-role" || request.Header.Get("apikey") != "service-role" {
			t.Fatalf("privileged storage headers missing")
		}
		if request.Header.Get("Content-Type") != "application/pdf" || request.Header.Get("x-upsert") != "true" {
			t.Fatalf("upload headers = %#v", request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "%PDF-test" {
			t.Fatalf("body = %q", body)
		}
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(server.Client(), baseURL, "service-role")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Upload(context.Background(), UploadRequest{UserID: userID, SourceID: sourceID, MIMEType: "application/pdf", Content: []byte("%PDF-test")})
	if err != nil {
		t.Fatal(err)
	}
	if result.ObjectPath == "" || result.ByteSize != 9 || !strings.HasSuffix(result.ObjectPath, ".pdf") {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientRejectsUnsupportedOrOversizeAttachmentBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client, err := New(server.Client(), baseURL, "service-role")
	if err != nil {
		t.Fatal(err)
	}
	request := UploadRequest{UserID: uuid.New(), SourceID: uuid.New(), MIMEType: "text/plain", Content: []byte("no")}
	if _, err := client.Upload(context.Background(), request); err == nil {
		t.Fatal("unsupported MIME type was accepted")
	}
	request.MIMEType = "application/pdf"
	request.Content = make([]byte, MaxBytes+1)
	if _, err := client.Upload(context.Background(), request); err == nil {
		t.Fatal("oversize attachment was accepted")
	}
	if requests != 0 {
		t.Fatalf("unexpected storage requests: %d", requests)
	}
}
