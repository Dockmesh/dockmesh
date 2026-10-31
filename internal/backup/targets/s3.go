package targets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint  string `json:"endpoint"` // "s3.amazonaws.com" or "minio.example.com:9000"
	Region    string `json:"region"`   // optional, defaults to "us-east-1"
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"` // optional path prefix inside the bucket
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	UseSSL    bool   `json:"use_ssl"`
	// CACert is an optional PEM bundle trusted in addition to the system roots.
	CACert string `json:"ca_cert,omitempty"`
}

type S3 struct {
	cfg    S3Config
	client *minio.Client
}

// NewS3 builds an S3 target. Compatible with AWS S3, MinIO, Backblaze
// B2 (S3 mode), Wasabi, anything that speaks the S3 API.
func NewS3(raw any) (*S3, error) {
	var cfg S3Config
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("s3 endpoint, bucket, access_key and secret_key are required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	tr, err := transferTransportWithCA(cfg.CACert)
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:    cfg.UseSSL,
		Region:    cfg.Region,
		Transport: tr,
	})
	if err != nil {
		return nil, err
	}
	return &S3{cfg: cfg, client: cli}, nil
}

// s3PartSize bounds the memory one upload uses: minio-go buffers one part
// at a time for streams of unknown size, and the 10,000-part limit puts
// the largest archive at about 320 GiB.
const s3PartSize = 32 << 20

// Open streams the archive as a multipart upload. A failed or aborted
// upload is cancelled by minio-go, so no partial object is left.
func (s *S3) Open(ctx context.Context, path string) (io.WriteCloser, error) {
	key := s.objectKey(path)
	return startPipeUpload(func(r io.Reader) error {
		_, err := s.client.PutObject(ctx, s.cfg.Bucket, key, r, -1, minio.PutObjectOptions{
			ContentType: "application/octet-stream",
			PartSize:    s3PartSize,
		})
		return err
	}), nil
}

func (s *S3) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, s.objectKey(path), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *S3) List(ctx context.Context, prefix string) ([]Entry, error) {
	var out []Entry
	full := s.objectKey(prefix)
	for obj := range s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    full,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, Entry{
			Path:    s.stripPrefix(obj.Key),
			Size:    obj.Size,
			ModTime: obj.LastModified,
		})
	}
	return out, nil
}

func (s *S3) Delete(ctx context.Context, path string) error {
	return s.client.RemoveObject(ctx, s.cfg.Bucket, s.objectKey(path), minio.RemoveObjectOptions{})
}

func (s *S3) objectKey(path string) string {
	if s.cfg.Prefix == "" {
		return path
	}
	return strings.TrimRight(s.cfg.Prefix, "/") + "/" + strings.TrimLeft(path, "/")
}

func (s *S3) stripPrefix(key string) string {
	if s.cfg.Prefix == "" {
		return key
	}
	prefix := strings.TrimRight(s.cfg.Prefix, "/") + "/"
	return strings.TrimPrefix(key, prefix)
}
