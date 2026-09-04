package attachmentstorage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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

func TestClientRejectsEmptySpoofedAndMismatchedContentBeforeStorage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client, err := New(server.Client(), baseURL, "service-role")
	if err != nil {
		t.Fatal(err)
	}
	base := UploadRequest{UserID: uuid.New(), SourceID: uuid.New()}
	testCases := []UploadRequest{
		{UserID: base.UserID, SourceID: base.SourceID, MIMEType: "application/pdf"},
		{UserID: base.UserID, SourceID: base.SourceID, MIMEType: "application/pdf", Content: []byte("<html>not a receipt</html>")},
		{UserID: base.UserID, SourceID: base.SourceID, MIMEType: "image/jpeg", Content: []byte("<html>not an image</html>")},
		{UserID: base.UserID, SourceID: base.SourceID, MIMEType: "image/jpeg", Content: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}},
		{UserID: base.UserID, SourceID: base.SourceID, MIMEType: "image/png", Content: []byte("%PDF-1.7")},
	}
	for _, request := range testCases {
		if _, err := client.Upload(context.Background(), request); err == nil {
			t.Fatalf("spoofed %q attachment was accepted", request.MIMEType)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid content reached Storage: %d requests", requests)
	}
}

func TestClientAcceptsMagicForEverySupportedAttachmentType(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client, err := New(server.Client(), baseURL, "service-role")
	if err != nil {
		t.Fatal(err)
	}
	heic := make([]byte, 24)
	binary.BigEndian.PutUint32(heic[:4], uint32(len(heic)))
	copy(heic[4:8], "ftyp")
	copy(heic[8:12], "mif1")
	copy(heic[16:20], "heic")
	testCases := map[string][]byte{
		"application/pdf": []byte("%PDF-1.7\n"),
		"image/bmp":       []byte{'B', 'M', 0x1a, 0x00},
		"image/jpeg":      []byte{0xff, 0xd8, 0xff, 0xe0},
		"image/png":       []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		"image/tiff":      []byte{'I', 'I', 0x2a, 0x00},
		"image/webp":      []byte{'R', 'I', 'F', 'F', 0x04, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P'},
		"image/heic":      heic,
	}
	for mimeType, content := range testCases {
		_, err := client.Upload(context.Background(), UploadRequest{
			UserID: uuid.New(), SourceID: uuid.New(), MIMEType: mimeType, Content: content,
		})
		if err != nil {
			t.Errorf("Upload(%q) error = %v", mimeType, err)
		}
	}
	if requests != len(testCases) {
		t.Fatalf("valid uploads = %d requests, want %d", requests, len(testCases))
	}
}

func TestClientDownloadsAndSignsOnlyOwnedAttachment(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	objectPath := userID.String() + "/" + sourceID.String() + "/receipt.pdf"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-role" {
			t.Fatal("missing service authorization")
		}
		switch r.URL.Path {
		case "/storage/v1/object/transaction-attachments/" + objectPath:
			_, _ = w.Write([]byte("receipt"))
		case "/storage/v1/object/sign/transaction-attachments/" + objectPath:
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"signedURL":"/object/sign/transaction-attachments/signed?token=opaque"}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client, err := New(server.Client(), base, "service-role")
	if err != nil {
		t.Fatal(err)
	}
	request := ObjectRequest{UserID: userID, SourceID: sourceID, ObjectPath: objectPath}
	content, err := client.Download(context.Background(), request)
	if err != nil || string(content) != "receipt" {
		t.Fatalf("Download() = %q, %v", content, err)
	}
	signed, err := client.SignURL(context.Background(), request, 60)
	if err != nil || signed != server.URL+"/storage/v1/object/sign/transaction-attachments/signed?token=opaque" {
		t.Fatalf("SignURL() = %q, %v", signed, err)
	}
	request.ObjectPath = userID.String() + "/other-source/receipt.pdf"
	if _, err := client.Download(context.Background(), request); err == nil {
		t.Fatal("cross-source download was accepted")
	}
}

func TestClientDeletesOwnedSourceObjectsInOneStorageRequest(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	paths := []string{
		userID.String() + "/" + sourceID.String() + "/a.png",
		userID.String() + "/" + sourceID.String() + "/b.pdf",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/storage/v1/object/"+Bucket {
			t.Fatalf("unexpected delete request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer service-secret" || r.Header.Get("apikey") != "service-secret" {
			t.Fatal("delete request omitted server-only authorization")
		}
		var body struct {
			Prefixes []string `json:"prefixes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(body.Prefixes, paths) {
			t.Fatalf("prefixes = %#v, want %#v", body.Prefixes, paths)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client, err := New(server.Client(), base, "service-secret")
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]ObjectRequest, 0, len(paths))
	for _, path := range paths {
		requests = append(requests, ObjectRequest{UserID: userID, SourceID: sourceID, ObjectPath: path})
	}
	if err = client.Delete(context.Background(), requests); err != nil {
		t.Fatal(err)
	}
	requests[1].SourceID = uuid.New()
	if err = client.Delete(context.Background(), requests); err == nil {
		t.Fatal("cross-source delete path was accepted")
	}
}

func TestScopeIDFromObjectPathUsesImmutableSecondSegment(t *testing.T) {
	userID, firstScopeID, secondScopeID := uuid.New(), uuid.New(), uuid.New()
	firstPath := userID.String() + "/" + firstScopeID.String() + "/first.png"
	secondPath := userID.String() + "/" + secondScopeID.String() + "/second.png"
	for path, want := range map[string]uuid.UUID{firstPath: firstScopeID, secondPath: secondScopeID} {
		got, err := ScopeIDFromObjectPath(userID, path)
		if err != nil || got != want {
			t.Fatalf("ScopeIDFromObjectPath(%q) = %s, %v; want %s", path, got, err, want)
		}
	}
	for _, path := range []string{
		uuid.NewString() + "/" + firstScopeID.String() + "/foreign.png",
		userID.String() + "/not-a-uuid/file.png",
		userID.String() + "/" + firstScopeID.String() + "/../foreign.png",
	} {
		if _, err := ScopeIDFromObjectPath(userID, path); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
}

func TestNormalizeSignedURLPreservesQueryAndRejectsUnsafeLocations(t *testing.T) {
	base, _ := url.Parse("https://project.example.test")
	client, err := New(&http.Client{}, base, "service-role")
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]string{
		"relative Storage path":  "/object/sign/transaction-attachments/owned/object.pdf?token=opaque",
		"complete relative path": "/storage/v1/object/sign/transaction-attachments/owned/object.pdf?token=opaque",
		"absolute same project":  "https://project.example.test/storage/v1/object/sign/transaction-attachments/owned/object.pdf?token=opaque",
	}
	for name, raw := range valid {
		t.Run(name, func(t *testing.T) {
			normalized, normalizeErr := client.normalizeSignedURL(raw)
			if normalizeErr != nil {
				t.Fatal(normalizeErr)
			}
			parsed, parseErr := url.Parse(normalized)
			if parseErr != nil || parsed.RawQuery != "token=opaque" || strings.Contains(parsed.Path, "%3F") {
				t.Fatalf("normalized signed URL did not preserve its query")
			}
		})
	}
	invalid := []string{
		"http://project.example.test/storage/v1/object/sign/transaction-attachments/owned/object.pdf?token=opaque",
		"https://other.example.test/storage/v1/object/sign/transaction-attachments/owned/object.pdf?token=opaque",
		"https://project.example.test/storage/v1/object/sign/other-bucket/owned/object.pdf?token=opaque",
		"https://project.example.test/storage/v1/object/sign/transaction-attachments/owned/object.pdf",
		"%",
	}
	for _, raw := range invalid {
		if normalized, normalizeErr := client.normalizeSignedURL(raw); normalizeErr == nil || normalized != "" {
			t.Fatal("unsafe signed URL was accepted")
		}
	}
}

func TestSigningHTTPErrorRetainsOnlySafeStatus(t *testing.T) {
	userID, sourceID := uuid.New(), uuid.New()
	objectPath := userID.String() + "/" + sourceID.String() + "/receipt.pdf"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"private object detail"}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client, err := New(server.Client(), base, "service-role")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SignURL(context.Background(), ObjectRequest{UserID: userID, SourceID: sourceID, ObjectPath: objectPath}, 300)
	status, ok := HTTPStatusCode(err)
	if !ok || status != http.StatusForbidden {
		t.Fatalf("HTTPStatusCode() = %d/%t, error %v", status, ok, err)
	}
	if strings.Contains(err.Error(), "private object detail") || strings.Contains(err.Error(), objectPath) {
		t.Fatal("Storage status error exposed response or object detail")
	}
}
