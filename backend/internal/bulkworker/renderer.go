package bulkworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
)

const (
	DefaultRenderTimeout = 30 * time.Second
	// Five contiguous pages share the provider's 5 MiB visual ceiling.
	DefaultMaxRenderedPage     = 1 * 1024 * 1024
	DefaultMaxRenderedDocument = 50 * 1024 * 1024
)

type Converter interface {
	RenderPDF(context.Context, []byte, int) ([][]byte, error)
	ConvertImage(context.Context, string, []byte) ([]byte, error)
}

// BoundedRenderer verifies every downloaded original and enforces output,
// page-count and wall-clock ceilings around conversion. Converter processes
// receive no URLs and are invoked without a shell.
type BoundedRenderer struct {
	Converter        Converter
	Timeout          time.Duration
	MaxPageBytes     int
	MaxDocumentBytes int
}

func (r BoundedRenderer) Prepare(ctx context.Context, files []OriginalFile, storage Storage, userID uuid.UUID) (PreparedDocument, error) {
	if storage == nil || userID == uuid.Nil || len(files) == 0 || len(files) > bulkimport.MaxFilesPerBatch {
		return PreparedDocument{}, errors.New("bulk renderer input is invalid")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultRenderTimeout
	}
	maxPage := r.MaxPageBytes
	if maxPage <= 0 {
		maxPage = DefaultMaxRenderedPage
	}
	maxDocument := r.MaxDocumentBytes
	if maxDocument <= 0 {
		maxDocument = DefaultMaxRenderedDocument
	}
	converter := r.Converter
	if converter == nil {
		converter = CommandConverter{}
	}
	renderCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := PreparedDocument{Pages: make([]PreparedPage, 0, len(files))}
	total := 0
	for fileIndex, file := range files {
		if file.SourceScopeID == uuid.Nil || file.ObjectPath == "" || file.MIMEType == "" {
			return PreparedDocument{}, errors.New("bulk original metadata is incomplete")
		}
		content, err := storage.Download(renderCtx, userID, file.SourceScopeID, file.ObjectPath)
		if err != nil {
			return PreparedDocument{}, fmt.Errorf("download bulk original: %w", err)
		}
		if file.ByteSize != int64(len(content)) || !matchesDigest(file.SHA256, content) {
			return PreparedDocument{}, fmt.Errorf("%w: size or checksum does not match reservation", ErrOriginalVerification)
		}
		var pages [][]byte
		switch file.MIMEType {
		case "application/pdf":
			if len(files) != 1 {
				return PreparedDocument{}, errors.New("a PDF must be its own document")
			}
			pages, err = converter.RenderPDF(renderCtx, content, bulkimport.MaxPages)
		case "image/jpeg", "image/png":
			var normalized []byte
			normalized, err = normalizeStandardImage(file.MIMEType, content)
			pages = [][]byte{normalized}
		case "image/bmp", "image/tiff", "image/webp", "image/heic":
			var normalized []byte
			normalized, err = converter.ConvertImage(renderCtx, file.MIMEType, content)
			pages = [][]byte{normalized}
		default:
			err = fmt.Errorf("%w: unsupported MIME type", ErrOriginalVerification)
		}
		if err != nil {
			if (file.MIMEType == "image/jpeg" || file.MIMEType == "image/png") && !errors.Is(err, ErrOriginalVerification) {
				err = fmt.Errorf("%w: %v", ErrOriginalVerification, err)
			}
			return PreparedDocument{}, err
		}
		if len(pages) == 0 || len(result.Pages)+len(pages) > bulkimport.MaxPages {
			return PreparedDocument{}, errors.New("rendered document has an invalid page count")
		}
		for pageIndex, page := range pages {
			if len(page) == 0 || len(page) > maxPage || total > maxDocument-len(page) {
				return PreparedDocument{}, errors.New("rendered document exceeds byte limits")
			}
			total += len(page)
			result.Pages = append(result.Pages, PreparedPage{
				ManifestPath:  fmt.Sprintf("file[%d].page[%d]", fileIndex, pageIndex+1),
				SourceScopeID: file.SourceScopeID,
				Filename:      fmt.Sprintf("file-%d-page-%d.png", fileIndex+1, pageIndex+1),
				MIMEType:      "image/png", Content: page,
			})
		}
	}
	return result, nil
}

func matchesDigest(expected string, content []byte) bool {
	digest := sha256.Sum256(content)
	return strings.EqualFold(strings.TrimSpace(expected), hex.EncodeToString(digest[:]))
}

func normalizeStandardImage(mime string, content []byte) ([]byte, error) {
	if mime == "image/jpeg" {
		decoded, err := jpeg.Decode(bytes.NewReader(content))
		if err != nil {
			return nil, errors.New("invalid JPEG original")
		}
		var output bytes.Buffer
		if err = png.Encode(&output, decoded); err != nil {
			return nil, err
		}
		return output.Bytes(), nil
	}
	decoded, err := png.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, errors.New("invalid PNG original")
	}
	var output bytes.Buffer
	if err = png.Encode(&output, decoded); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// CommandConverter uses deployment-provided, non-networked converter binaries.
// pdftoppm is portable across supported worker images; image conversion prefers
// ImageMagick and uses sips only on macOS development hosts.
type CommandConverter struct{}

func (CommandConverter) RenderPDF(ctx context.Context, content []byte, maxPages int) ([][]byte, error) {
	directory, err := os.MkdirTemp("", "wealth-builder-bulk-pdf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	input, prefix := filepath.Join(directory, "source.pdf"), filepath.Join(directory, "page")
	if err = os.WriteFile(input, content, 0o600); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "pdftoppm", "-png", "-r", "144", "-f", "1", "-l", fmt.Sprint(maxPages+1), input, prefix)
	command.Dir = directory
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + directory}
	if output, runErr := command.CombinedOutput(); runErr != nil {
		return nil, fmt.Errorf("rasterize PDF: %w (%s)", runErr, boundedCommandOutput(output))
	}
	paths, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) == 0 || len(paths) > maxPages {
		return nil, errors.New("PDF page count is outside limits")
	}
	pages := make([][]byte, 0, len(paths))
	for _, path := range paths {
		page, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		pages = append(pages, page)
	}
	return pages, nil
}

func (CommandConverter) ConvertImage(ctx context.Context, _ string, content []byte) ([]byte, error) {
	directory, err := os.MkdirTemp("", "wealth-builder-bulk-image-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	input, output := filepath.Join(directory, "source.image"), filepath.Join(directory, "converted.png")
	if err = os.WriteFile(input, content, 0o600); err != nil {
		return nil, err
	}
	name, args := "magick", []string{input, "-strip", output}
	if runtime.GOOS == "darwin" {
		name, args = "sips", []string{"-s", "format", "png", input, "--out", output}
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + directory}
	if commandOutput, runErr := command.CombinedOutput(); runErr != nil {
		return nil, fmt.Errorf("convert image: %w (%s)", runErr, boundedCommandOutput(commandOutput))
	}
	converted, err := os.ReadFile(output)
	if err != nil {
		return nil, err
	}
	return converted, nil
}

func boundedCommandOutput(output []byte) string {
	if len(output) > 512 {
		output = output[:512]
	}
	return strings.TrimSpace(string(output))
}
