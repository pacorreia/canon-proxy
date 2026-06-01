package backend

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/pacorreia/canon-proxy/internal/config"
)

type S3Backend struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3Backend(cfg config.S3Config) (*S3Backend, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 bucket is required")
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("s3 region is required")
	}

	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		if cfg.AccessKey == "" || cfg.SecretKey == "" {
			return nil, fmt.Errorf("s3 access_key and secret_key must both be set (or both empty)")
		}
		provider := credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
		options = append(options, awsconfig.WithCredentialsProvider(provider))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), options...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	return &S3Backend{
		client: s3.NewFromConfig(awsCfg),
		bucket: cfg.Bucket,
		prefix: strings.TrimPrefix(cfg.Prefix, "/"),
	}, nil
}

func (b *S3Backend) Name() string {
	return "s3"
}

func (b *S3Backend) Upload(ctx context.Context, filename string, r io.Reader) error {
	key := path.Join(b.prefix, filename)

	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   r,
	})
	if err != nil {
		return fmt.Errorf("upload object to s3 bucket %s key %s: %w", b.bucket, key, err)
	}

	return nil
}
