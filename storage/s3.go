// Copyright 2022 The TVL Contributors
// SPDX-License-Identifier: Apache-2.0

// AWS S3 storage backend for Nixery.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
)

type S3Backend struct {
	bucket     string
	region     string
	client     *s3.Client
	uploader   *manager.Uploader
	downloader *manager.Downloader
}

// NewS3Backend constructs a new S3 backend based on the configured
// environment variables.
func NewS3Backend() (*S3Backend, error) {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET must be configured for S3 usage")
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1" // Default region
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		log.WithError(err).Fatal("failed to load AWS configuration")
		return nil, err
	}

	client := s3.NewFromConfig(cfg)

	// Test bucket access
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		log.WithError(err).WithField("bucket", bucket).Error("could not access configured S3 bucket")
		return nil, err
	}

	uploader := manager.NewUploader(client)
	downloader := manager.NewDownloader(client)

	return &S3Backend{
		bucket:     bucket,
		region:     region,
		client:     client,
		uploader:   uploader,
		downloader: downloader,
	}, nil
}

func (b *S3Backend) Name() string {
	return "AWS S3 (" + b.bucket + ")"
}

func (b *S3Backend) Persist(ctx context.Context, path, contentType string, f Persister) (string, int64, error) {
	// Create a pipe to stream data to S3
	pr, pw := io.Pipe()

	var hash string
	var size int64
	var err error

	// Upload in a goroutine
	uploadDone := make(chan error, 1)
	go func() {
		defer close(uploadDone)

		input := &s3.PutObjectInput{
			Bucket: aws.String(b.bucket),
			Key:    aws.String(path),
			Body:   pr,
		}

		if contentType != "" {
			input.ContentType = aws.String(contentType)
		}

		_, uploadErr := b.uploader.Upload(ctx, input)
		uploadDone <- uploadErr
	}()

	// Write data and get hash/size
	hash, size, err = f(pw)
	pw.Close() // Close the write end of the pipe

	if err != nil {
		pr.CloseWithError(err) // Close read end with error
		<-uploadDone           // Wait for upload to finish
		log.WithError(err).WithField("path", path).Error("failed to write data for S3 upload")
		return hash, size, err
	}

	// Wait for upload to complete
	uploadErr := <-uploadDone
	if uploadErr != nil {
		log.WithError(uploadErr).WithField("path", path).Error("failed to upload to S3")
		return hash, size, uploadErr
	}

	return hash, size, nil
}

func (b *S3Backend) Fetch(ctx context.Context, path string) (io.ReadCloser, error) {
	// Check if object exists first
	_, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, err
	}

	resp, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}

func (b *S3Backend) Move(ctx context.Context, old, new string) error {
	// Copy object to new location
	_, err := b.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(b.bucket),
		Key:        aws.String(new),
		CopySource: aws.String(b.bucket + "/" + old),
	})
	if err != nil {
		return err
	}

	// Delete the old object
	_, err = b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(old),
	})
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"old": old,
			"new": new,
		}).Warn("failed to delete old object after move")
		// Don't return error as the copy succeeded
	}

	return nil
}

func (b *S3Backend) Serve(digest string, r *http.Request, w http.ResponseWriter) error {
	url, err := b.constructLayerUrl(digest)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"digest": digest,
			"bucket": b.bucket,
		}).Error("failed to generate S3 URL")
		return err
	}

	log.WithField("digest", digest).Info("redirecting blob request to S3")

	w.Header().Set("Location", url)
	w.WriteHeader(303)
	return nil
}

// constructLayerUrl creates a presigned URL for the layer object in S3
func (b *S3Backend) constructLayerUrl(digest string) (string, error) {
	key := "layers/" + digest

	// Check if we should use presigned URLs (recommended for private buckets)
	usePresignedUrls := os.Getenv("S3_USE_PRESIGNED_URLS")
	if usePresignedUrls == "false" {
		// Use direct public URL (bucket must be publicly readable)
		if strings.Contains(b.bucket, ".") {
			// Use path-style URL for buckets with dots
			return fmt.Sprintf("https://s3.%s.amazonaws.com/%s/%s", b.region, b.bucket, key), nil
		} else {
			// Use virtual-hosted-style URL
			return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", b.bucket, b.region, key), nil
		}
	}

	// Create presigned URL (default behavior)
	presignClient := s3.NewPresignClient(b.client)

	presignedReq, err := presignClient.PresignGetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = 5 * time.Minute
	})

	if err != nil {
		return "", err
	}

	return presignedReq.URL, nil
}
