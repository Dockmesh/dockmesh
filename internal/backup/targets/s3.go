package targets

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint  string `json:"endpoint"`  // "s3.amazonaws.com" or "minio.example.com:9000"
	Region    string `json:"region"`    // optional, defaults to "us-east-1"
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`    // optional path prefix inside the bucket
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	UseSSL    bool   `json:"use_ssl"`
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
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	return &S3{cfg: cfg, client: cli}, nil
}

// s3Upload buffers writes to a temp file and uploads on Close. Streaming
// directly via io.Pipe is possible but minio-go needs the size up front
// for non-multipart uploads; the temp file lets us hash + size + upload
// in one pass without keeping the whole archive in memory.
type s3Upload struct {
	file   *os.File
	target *S3
	key    string
	ctx    context.Context
}

func (u *s3Upload) Write(p []byte) (int, error) { return u.file.Write(p) }

func (u *s3Upload) Close() error {
	defer os.Remove(u.file.Name())
	if _, err := u.file.Seek(0, 0); err != nil {
		u.file.Close()
		return err
	}
	info, err := u.file.Stat()
	if err != nil {
		u.file.Close()
		return err
	}
	_, err = u.target.client.PutObject(u.ctx, u.target.cfg.Bucket, u.key, u.file, info.Size(),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	u.file.Close()
	return err
}

func (s *S3) Open(ctx context.Context, path string) (io.WriteCloser, error) {
	f, err := os.CreateTemp("", "dockmesh-s3-")
	if err != nil {
		return nil, err
	}
	return &s3Upload{file: f, target: s, key: s.objectKey(path), ctx: ctx}, nil
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
