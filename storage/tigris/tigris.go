// Package tigris implements the storage contract with Tigris's S3-compatible API.
package tigris

import (
	"bytes"
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/darron/fbhook/storage"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Endpoint, Bucket, Region, Prefix, AccessKeyID, SecretAccessKey string
	HTTPClient                                                     *http.Client
}

func FromEnv() Config {
	return Config{Endpoint: os.Getenv("AWS_ENDPOINT_URL_S3"), Bucket: os.Getenv("BUCKET_NAME"), Region: os.Getenv("AWS_REGION"), Prefix: os.Getenv("FBHOOK_STORAGE_PREFIX"), AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY")}
}

type Store struct {
	client         *s3.Client
	bucket, prefix string
}

func New(_ context.Context, c Config) (*Store, error) {
	if c.Endpoint == "" {
		c.Endpoint = "https://t3.storage.dev"
	}
	if c.Region == "" {
		c.Region = "auto"
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.Bucket == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, errors.New("tigris endpoint, bucket and credentials are required")
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	hc := *c.HTTPClient
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	cfg := aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""), HTTPClient: &hc, RetryMaxAttempts: 1}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })
	prefix := strings.Trim(c.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &Store{client: client, bucket: c.Bucket, prefix: prefix}, nil
}
func (s *Store) Get(ctx context.Context, key string) (storage.Object, error) {
	out, e := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key)})
	if e != nil {
		return storage.Object{}, mapError(e)
	}
	defer func() { _ = out.Body.Close() }()
	b, e := io.ReadAll(io.LimitReader(out.Body, 2*1024*1024+1))
	if e != nil || len(b) > 2*1024*1024 {
		return storage.Object{}, errors.New("object read failed or exceeded limit")
	}
	if out.ETag == nil {
		return storage.Object{}, errors.New("object has no version")
	}
	return storage.Object{Body: b, Version: *out.ETag}, nil
}
func (s *Store) Create(ctx context.Context, key string, body []byte) (string, error) {
	return s.put(ctx, key, body, "", true)
}
func (s *Store) Replace(ctx context.Context, key string, body []byte, version string) (string, error) {
	if version == "" {
		return "", storage.ErrConflict
	}
	return s.put(ctx, key, body, version, false)
}
func (s *Store) put(ctx context.Context, key string, body []byte, version string, create bool) (string, error) {
	in := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key), Body: bytes.NewReader(body), ContentType: aws.String("application/json"), CacheControl: aws.String("no-store")}
	if create {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(version)
	}
	out, e := s.client.PutObject(ctx, in)
	if e != nil {
		return "", mapError(e)
	}
	if out.ETag == nil {
		return "", errors.New("write returned no version")
	}
	return *out.ETag, nil
}
func (s *Store) List(ctx context.Context, prefix, cursor string, limit int) (storage.Page, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	in := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(s.prefix + prefix), MaxKeys: aws.Int32(int32(limit))}
	if cursor != "" {
		in.ContinuationToken = aws.String(cursor)
	}
	out, e := s.client.ListObjectsV2(ctx, in)
	if e != nil {
		return storage.Page{}, mapError(e)
	}
	page := storage.Page{Keys: []string{}}
	for _, o := range out.Contents {
		if o.Key != nil && strings.HasPrefix(*o.Key, s.prefix+prefix) {
			page.Keys = append(page.Keys, strings.TrimPrefix(*o.Key, s.prefix))
		}
	}
	if aws.ToBool(out.IsTruncated) {
		if out.NextContinuationToken == nil || *out.NextContinuationToken == cursor {
			return storage.Page{}, errors.New("invalid storage pagination")
		}
		page.Cursor = *out.NextContinuationToken
	}
	return page, nil
}
func mapError(e error) error {
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return e
	}
	var api smithy.APIError
	if errors.As(e, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return storage.ErrNotFound
		case "PreconditionFailed", "ConditionalRequestConflict":
			return storage.ErrConflict
		}
	}
	return errors.New("object storage operation failed")
}
