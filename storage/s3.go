// Copyright The TVL Contributors
// SPDX-License-Identifier: Apache-2.0

// Amazon S3 storage backend for Nixery.
package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Backend struct {
	bucket         string
	linkExpiration time.Duration
	client         *s3.Client
	uploader       *manager.Uploader
}

// Constructs a new S3 bucket backend based on the configured
// environment variables.
func NewS3Backend() (*S3Backend, error) {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET must be configured for S3 usage")
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		// Default value
		region = "us-east-1"
	}

	// Load AWS config from environment variables
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}

	// Support custom S3-compatible endpoints (e.g., MinIO, LocalStack)
	if endpoint := os.Getenv("S3_ENDPOINT"); endpoint != "" {
		opts = append(opts, config.WithBaseEndpoint(endpoint))
	}

	// Support static credentials for S3-compatible services
	if accessKey := os.Getenv("S3_ACCESS_KEY"); accessKey != "" {
		secretKey := os.Getenv("S3_SECRET_KEY")
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}

	// Default to path-style addressing, which is required by most
	// S3-compatible services, but vendors such as AWS may need
	// virtual-host addressing instead.
	usePathStyle := true
	if v := os.Getenv("S3_USE_PATH_STYLE"); v != "" {
		var err error
		usePathStyle, err = strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("failed to parse S3_USE_PATH_STYLE: %w", err)
		}
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = usePathStyle
	})

	linkExpires := os.Getenv("S3_LINK_EXPIRATION")
	if linkExpires == "" {
		linkExpires = "5m"
	}

	linkExpiration, err := time.ParseDuration(linkExpires)
	if err != nil || linkExpiration < 0 {
		return nil, fmt.Errorf("failed to parse link expiration: %w", err)
	}

	return &S3Backend{
		bucket:         bucket,
		linkExpiration: linkExpiration,
		client:         client,
		uploader:       manager.NewUploader(client),
	}, nil
}

func (b *S3Backend) Name() string {
	return "S3 (" + b.bucket + ")"
}

func (b *S3Backend) Persist(ctx context.Context, key, contentType string, f Persister) (string, int64, error) {
	// Use a pipe to connect the persister function to the S3 uploader
	pr, pw := io.Pipe()
	done := make(chan error, 1)

	var hash string
	var size int64
	var uploadErr error

	// Run the persister function in a goroutine to avoid deadlocks
	go func() {
		hash, size, uploadErr = f(pw)
		pw.Close()
		done <- uploadErr
	}()

	_, err := b.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(b.bucket),
		Key:         aws.String(key),
		Body:        pr,
		ContentType: aws.String(contentType),
	})

	if err != nil {
		slog.Error("failed to write to S3", "err", err, "key", key)
		return "", 0, err
	}

	// Wait for the persister to finish
	if err := <-done; err != nil {
		slog.Error("failed to complete S3 upload", "err", err, "key", key)
		return "", 0, err
	}

	return hash, size, nil
}

func (b *S3Backend) Fetch(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}

// Move copies an object to a new location and deletes the original.
// S3 doesn't have a native rename operation, so we implement it as
// copy + delete (similar to the GCS backend).
func (b *S3Backend) Move(ctx context.Context, old, new string) error {
	// Copy the object to the new location
	_, err := b.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(b.bucket),
		CopySource: aws.String(b.bucket + "/" + old),
		Key:        aws.String(new),
	})
	if err != nil {
		slog.Error("failed to copy S3 object", "err", err, "old", old, "new", new)
		return err
	}

	// Delete the old object
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(old),
	}); err != nil {
		slog.Warn("failed to delete moved object", "err", err, "old", old)
		// Don't return error - the copy succeeded, which is the important part
	}

	return nil
}

func (b *S3Backend) Serve(digest string, r *http.Request, w http.ResponseWriter) error {
	key := "layers/" + digest

	// Generate a pre-signed URL for the object
	presignClient := s3.NewPresignClient(b.client)
	presignedURL, err := presignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = b.linkExpiration
	})

	if err != nil {
		slog.Error("failed to pre-sign S3 URL", "err", err, "digest", digest, "bucket", b.bucket)
		return err
	}

	slog.Info("redirecting blob request to S3", "digest", digest, "bucket", b.bucket)

	w.Header().Set("Location", presignedURL.URL)
	w.WriteHeader(http.StatusSeeOther)
	return nil
}
