package attachmentstorage

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCreateSignedUploadURLUsesServiceCredentialOnlyForSigning(t *testing.T) {
	userID, scopeID, fileID := uuid.New(), uuid.New(), uuid.New()
	path := strings.Join([]string{userID.String(), scopeID.String(), fileID.String() + ".pdf"}, "/")
	clientHTTP := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/storage/v1/object/upload/sign/"+Bucket+"/"+path {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer service-secret" || r.Header.Get("apikey") != "service-secret" {
			t.Fatal("signing request omitted service authentication")
		}
		body := `{"url":"https://project.supabase.co/storage/v1/object/upload/sign/` + Bucket + `/` + path + `?token=browser-token"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	base, _ := url.Parse("https://project.supabase.co")
	client, err := New(clientHTTP, base, "service-secret")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := client.CreateSignedUploadURL(context.Background(), BulkObjectRequest(userID, scopeID, path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(signed, "service-secret") || !strings.Contains(signed, "token=browser-token") {
		t.Fatalf("unsafe signed URL: %s", signed)
	}
}

func TestCreateSignedUploadURLNormalizesProviderRelativePath(t *testing.T) {
	userID, scopeID, fileID := uuid.New(), uuid.New(), uuid.New()
	path := strings.Join([]string{userID.String(), scopeID.String(), fileID.String() + ".png"}, "/")
	clientHTTP := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"url":"/object/upload/sign/` + Bucket + `/` + path + `?token=browser-token","token":"browser-token"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	base, _ := url.Parse("https://project.supabase.co")
	client, err := New(clientHTTP, base, "service-secret")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := client.CreateSignedUploadURL(context.Background(), BulkObjectRequest(userID, scopeID, path))
	if err != nil {
		t.Fatal(err)
	}
	want := "https://project.supabase.co/storage/v1/object/upload/sign/" + Bucket + "/" + path + "?token=browser-token"
	if signed != want {
		t.Fatalf("signed URL = %q, want provider-relative URL normalized to the project Storage endpoint", signed)
	}
}

func TestCreateSignedUploadURLRejectsForeignPathAndHost(t *testing.T) {
	base, _ := url.Parse("https://project.supabase.co")
	client, _ := New(http.DefaultClient, base, "service-secret")
	userID, scopeID := uuid.New(), uuid.New()
	if _, err := client.CreateSignedUploadURL(context.Background(), BulkObjectRequest(userID, scopeID, uuid.NewString()+"/"+scopeID.String()+"/file.pdf")); err == nil {
		t.Fatal("expected foreign owner path to be rejected")
	}
}

func TestStatObjectReturnsBoundedMetadata(t *testing.T) {
	userID, scopeID := uuid.New(), uuid.New()
	path := userID.String() + "/" + scopeID.String() + "/file.png"
	clientHTTP := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodHead {
			t.Fatalf("method = %s", r.Method)
		}
		headers := make(http.Header)
		headers.Set("Content-Length", "1200")
		headers.Set("Content-Type", "image/png")
		headers.Set("ETag", `"safe-etag"`)
		return &http.Response{StatusCode: http.StatusOK, Header: headers, ContentLength: 1200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	base, _ := url.Parse("https://project.supabase.co")
	client, _ := New(clientHTTP, base, "service-secret")
	metadata, err := client.StatObject(context.Background(), BulkObjectRequest(userID, scopeID, path))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ByteSize != 1200 || metadata.ContentType != "image/png" || metadata.ETag != `"safe-etag"` {
		t.Fatalf("metadata = %#v", metadata)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
