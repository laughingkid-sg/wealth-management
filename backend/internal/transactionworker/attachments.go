package transactionworker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhengteck/wealth-builder/backend/internal/providers"
)

// renderVisualAttachment emits only image inputs accepted by the provider.
// PDFs are rasterized into at most three pages in a scoped temporary directory;
// failure deliberately skips an optional attachment rather than failing email
// parsing. No source bytes are retained after this function returns.
func renderVisualAttachment(ctx context.Context, filename, mimeType string, content []byte) []providers.AttachmentInput {
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return []providers.AttachmentInput{{Filename: filename, MIMEType: mimeType, Content: content}}
	case "application/pdf":
		return renderPDF(ctx, filename, content)
	case "image/bmp", "image/tiff", "image/heic":
		return convertImageToPNG(ctx, filename, content)
	default:
		return nil
	}
}

// convertImageToPNG uses macOS sips, the available local image converter, in
// the same short-lived directory model as PDF rasterisation. Qwen only sees
// PNG/JPEG/WebP/GIF data URLs; unsupported source formats are never sent as a
// misleading MIME type.
func convertImageToPNG(ctx context.Context, filename string, content []byte) []providers.AttachmentInput {
	if len(content) == 0 || len(content) > 5*1024*1024 {
		return nil
	}
	dir, err := os.MkdirTemp("", "wealth-builder-image-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "source"+filepath.Ext(filename))
	output := filepath.Join(dir, "converted.png")
	if os.WriteFile(input, content, 0600) != nil {
		return nil
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if exec.CommandContext(commandCtx, "sips", "-s", "format", "png", input, "--out", output).Run() != nil {
		return nil
	}
	converted, err := os.ReadFile(output)
	if err != nil || len(converted) == 0 || len(converted) > 5*1024*1024 {
		return nil
	}
	return []providers.AttachmentInput{{Filename: strings.TrimSuffix(filename, filepath.Ext(filename)) + ".png", MIMEType: "image/png", Content: converted}}
}

func renderPDF(ctx context.Context, filename string, content []byte) []providers.AttachmentInput {
	if len(content) == 0 || len(content) > 5*1024*1024 {
		return nil
	}
	dir, err := os.MkdirTemp("", "wealth-builder-pdf-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "source.pdf")
	if os.WriteFile(input, content, 0600) != nil {
		return nil
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	outputPrefix := filepath.Join(dir, "page")
	command := exec.CommandContext(commandCtx, "pdftoppm", "-png", "-f", "1", "-l", "3", input, outputPrefix)
	if command.Run() != nil {
		return nil
	}
	result := make([]providers.AttachmentInput, 0, 3)
	for page := 1; page <= 3; page++ {
		path := outputPrefix + "-" + strconv.Itoa(page) + ".png"
		image, readErr := os.ReadFile(path)
		if readErr != nil || len(image) == 0 || len(image) > 5*1024*1024 {
			continue
		}
		result = append(result, providers.AttachmentInput{Filename: strings.TrimSuffix(filename, filepath.Ext(filename)) + "-page-" + strconv.Itoa(page) + ".png", MIMEType: "image/png", Content: image})
	}
	return result
}
