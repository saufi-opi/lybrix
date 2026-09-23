// Package objectstore: MinIO/S3 client helpers — presign, upload, download
// (PRD §6.1).
//
// The API never buffers file bytes — clients PUT directly to MinIO via a
// presigned URL; workers download to disk only.
//
// Key layout copied exactly from 1.0 (libs/core s3.py): raw key = `{doc_id}.pdf`
// in bucket `raw`; parsed key = `{doc_id}/{shard_idx}.md` in bucket `parsed` —
// the bucket name carries the raw/parsed split, there is NO `raw/` or
// `parsed/` path prefix on the keys.
package objectstore

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Client wraps the S3 API client with the endpoint/credentials from settings.
type Client struct {
	api        *s3.Client
	publicAPI  *s3.Client
	uploader   *manager.Uploader
	downloader *manager.Downloader
}

// New builds a path-style S3 client against MinIO (S3v4 signing).
func New(ctx context.Context, endpoint, accessKey, secretKey string) (*Client, error) {
	return NewWithPublicEndpoint(ctx, endpoint, endpoint, accessKey, secretKey)
}

func NewWithPublicEndpoint(ctx context.Context, endpoint, publicEndpoint, accessKey, secretKey string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, err
	}
	api := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	pubEndpoint := publicEndpoint
	if pubEndpoint == "" {
		pubEndpoint = endpoint
	}
	publicAPI := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(pubEndpoint)
		o.UsePathStyle = true
	})
	return &Client{
		api:        api,
		publicAPI:  publicAPI,
		uploader:   manager.NewUploader(api),
		downloader: manager.NewDownloader(api),
	}, nil
}

// RawKey is the object key for an uploaded source PDF: s3://{bucket}/raw/{doc_id}.pdf.
func RawKey(docID string) string { return "raw/" + docID + ".pdf" }

// ParsedKey is the object key for a parsed shard markdown:
// s3://{bucket}/parsed/{doc}/{idx}.md.
func ParsedKey(docID string, shardIdx int) string {
	return fmt.Sprintf("parsed/%s/%d.md", docID, shardIdx)
}

// PresignPut returns a presigned PUT URL valid for expires.
func (c *Client) PresignPut(ctx context.Context, bucket, key string, expires time.Duration) (string, error) {
	req := &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	clientToUse := c.api
	if c.publicAPI != nil {
		clientToUse = c.publicAPI
	}
	ps := s3.NewPresignClient(clientToUse)
	out, err := ps.PresignPutObject(ctx, req, s3.WithPresignExpires(expires))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}

// DownloadTo streams the object to a local file path.
func (c *Client) DownloadTo(ctx context.Context, bucket, key, destPath string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = c.downloader.Download(ctx, f, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	return err
}

// GetReader opens the object for streaming reads (commit verify path).
func (c *Client) GetReader(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// UploadText PUTs a UTF-8 text object (markdown shard payloads).
func (c *Client) UploadText(ctx context.Context, bucket, key, text, contentType string) error {
	if contentType == "" {
		contentType = "text/markdown"
		if ext := filepath.Ext(key); ext == ".json" {
			contentType = mime.TypeByExtension(ext)
		}
	}
	_, err := c.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(text),
		ContentType: aws.String(contentType),
	})
	return err
}

// GetText reads a whole small object into memory (shard markdown fetch).
func (c *Client) GetText(ctx context.Context, bucket, key string) (string, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return "", err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func stringReader(s string) io.Reader { return io.NopCloser(newByteReader(s)) }

type byteReader struct {
	s string
	i int
}

func newByteReader(s string) *byteReader { return &byteReader{s: s} }

func (b *byteReader) Read(p []byte) (int, error) {
	if b.i >= len(b.s) {
		return 0, io.EOF
	}
	n := copy(p, b.s[b.i:])
	b.i += n
	return n, nil
}

// UploadFile streams a local file into an object — manager.Uploader takes
// the multipart path automatically past its part threshold (>32 MiB), so
// the API never buffers a whole large body in RAM.
func (c *Client) UploadFile(ctx context.Context, bucket, key, contentType, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = c.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        f,
		ContentType: aws.String(contentType),
	})
	return err
}
