package bulkworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/google/uuid"
)

type renderStorage struct {
	content        []byte
	downloadScopes *[]uuid.UUID
}

func (s renderStorage) Download(_ context.Context, _ uuid.UUID, scopeID uuid.UUID, _ string) ([]byte, error) {
	if s.downloadScopes != nil {
		*s.downloadScopes = append(*s.downloadScopes, scopeID)
	}
	return append([]byte(nil), s.content...), nil
}
func (renderStorage) Upload(context.Context, uuid.UUID, uuid.UUID, string, []byte) (string, error) {
	return "unused", nil
}

type renderConverter struct {
	pages [][]byte
	calls int
}

func (c *renderConverter) RenderPDF(context.Context, []byte, int) ([][]byte, error) {
	c.calls++
	return c.pages, nil
}
func (c *renderConverter) ConvertImage(context.Context, string, []byte) ([]byte, error) {
	c.calls++
	return c.pages[0], nil
}

func TestBoundedRendererVerifiesAndOrdersGroupedImages(t *testing.T) {
	content := testPNG(t)
	digest := sha256.Sum256(content)
	userID, firstScopeID, secondScopeID := uuid.New(), uuid.New(), uuid.New()
	downloading := make([]uuid.UUID, 0, 2)
	storage := renderStorage{content: content, downloadScopes: &downloading}
	rendered, err := (BoundedRenderer{}).Prepare(context.Background(), []OriginalFile{
		{SourceScopeID: firstScopeID, ObjectPath: userID.String() + "/" + firstScopeID.String() + "/a.png", MIMEType: "image/png", ByteSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])},
		{SourceScopeID: secondScopeID, ObjectPath: userID.String() + "/" + secondScopeID.String() + "/b.png", MIMEType: "image/png", ByteSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])},
	}, storage, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Pages) != 2 || rendered.Pages[0].ManifestPath != "file[0].page[1]" || rendered.Pages[1].ManifestPath != "file[1].page[1]" {
		t.Fatalf("pages = %#v", rendered.Pages)
	}
	if len(downloading) != 2 || downloading[0] != firstScopeID || downloading[1] != secondScopeID {
		t.Fatalf("download scopes = %#v", downloading)
	}
}

func TestBoundedRendererRejectsChecksumAndOutputLimit(t *testing.T) {
	content := []byte("%PDF-test")
	userID, scopeID := uuid.New(), uuid.New()
	file := OriginalFile{SourceScopeID: scopeID, ObjectPath: userID.String() + "/" + scopeID.String() + "/a.pdf", MIMEType: "application/pdf", ByteSize: int64(len(content)), SHA256: "wrong"}
	if _, err := (BoundedRenderer{Converter: &renderConverter{pages: [][]byte{{1}}}}).Prepare(context.Background(), []OriginalFile{file}, renderStorage{content: content}, userID); err == nil {
		t.Fatal("expected checksum rejection")
	}
	digest := sha256.Sum256(content)
	file.SHA256 = hex.EncodeToString(digest[:])
	if _, err := (BoundedRenderer{Converter: &renderConverter{pages: [][]byte{{1, 2}}}, MaxPageBytes: 1}).Prepare(context.Background(), []OriginalFile{file}, renderStorage{content: content}, userID); err == nil {
		t.Fatal("expected rendered-page limit rejection")
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.Black)
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
