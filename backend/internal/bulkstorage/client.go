// Package bulkstorage adapts the private attachment client to Bulk Import.
package bulkstorage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/attachmentstorage"
	"github.com/zhengteck/wealth-builder/backend/internal/bulkimport"
)

type Client struct{ Storage *attachmentstorage.Client }

func (c Client) CreateSignedUpload(ctx context.Context, userID, scopeID uuid.UUID, path string, applicationLifetime time.Duration) (string, error) {
	if c.Storage == nil || applicationLifetime < time.Minute || applicationLifetime > time.Hour {
		return "", errors.New("bulk upload storage is not configured")
	}
	return c.Storage.CreateSignedUploadURL(ctx, attachmentstorage.BulkObjectRequest(userID, scopeID, path))
}

func (c Client) Stat(ctx context.Context, userID, scopeID uuid.UUID, path string) (bulkimport.ObjectMetadata, error) {
	if c.Storage == nil {
		return bulkimport.ObjectMetadata{}, errors.New("bulk upload storage is not configured")
	}
	metadata, err := c.Storage.StatObject(ctx, attachmentstorage.BulkObjectRequest(userID, scopeID, path))
	if err != nil {
		return bulkimport.ObjectMetadata{}, err
	}
	return bulkimport.ObjectMetadata{ByteSize: metadata.ByteSize, ContentType: metadata.ContentType, ETag: metadata.ETag}, nil
}

func (c Client) Download(ctx context.Context, userID, _ uuid.UUID, path string) ([]byte, error) {
	if c.Storage == nil {
		return nil, errors.New("bulk upload storage is not configured")
	}
	request, err := attachmentstorage.ObjectRequestFromPath(userID, path)
	if err != nil {
		return nil, err
	}
	return c.Storage.Download(ctx, request)
}

func (c Client) Upload(ctx context.Context, userID, scopeID uuid.UUID, mimeType string, content []byte) (string, error) {
	if c.Storage == nil {
		return "", errors.New("bulk upload storage is not configured")
	}
	result, err := c.Storage.Upload(ctx, attachmentstorage.UploadRequest{
		UserID: userID, SourceID: scopeID, MIMEType: mimeType, Content: content,
	})
	if err != nil {
		return "", err
	}
	return result.ObjectPath, nil
}

func (c Client) SignURL(ctx context.Context, userID, _ uuid.UUID, path string, expiresInSeconds int) (string, error) {
	if c.Storage == nil {
		return "", errors.New("bulk upload storage is not configured")
	}
	request, err := attachmentstorage.ObjectRequestFromPath(userID, path)
	if err != nil {
		return "", err
	}
	return c.Storage.SignURL(ctx, request, expiresInSeconds)
}
