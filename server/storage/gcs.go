// Google Cloud Storage backend for Nixery.
package storage

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"time"

	"cloud.google.com/go/storage"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2/google"
)

// HTTP client to use for direct calls to APIs that are not part of the SDK
var client = &http.Client{}

// API scope needed for renaming objects in GCS
const gcsScope = "https://www.googleapis.com/auth/devstorage"

type GCSBackend struct {
	bucket  string
	handle  *storage.BucketHandle
	signing *storage.SignedURLOptions
}

// Constructs a new GCS bucket backend based on the configured
// environment variables.
func NewGCSBackend() (*GCSBackend, error) {
	bucket := os.Getenv("GCS_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("GCS_BUCKET must be configured for GCS usage")
	}

	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		log.WithError(err).Fatal("failed to set up Cloud Storage client")
	}

	handle := client.Bucket(bucket)

	if _, err := handle.Attrs(ctx); err != nil {
		log.WithError(err).WithField("bucket", bucket).Error("could not access configured bucket")
		return nil, err
	}

	signing, err := signingOptsFromEnv()
	if err != nil {
		log.WithError(err).Error("failed to configure GCS bucket signing")
		return nil, err
	}

	return &GCSBackend{
		bucket:  bucket,
		handle:  handle,
		signing: signing,
	}, nil
}

func (b *GCSBackend) Name() string {
	return "Google Cloud Storage (" + b.bucket + ")"
}

func (b *GCSBackend) Persist(path string, f func(io.Writer) (string, int64, error)) (string, int64, error) {
	ctx := context.Background()
	obj := b.handle.Object(path)
	w := obj.NewWriter(ctx)

	hash, size, err := f(w)
	if err != nil {
		log.WithError(err).WithField("path", path).Error("failed to upload to GCS")
		return hash, size, err
	}

	return hash, size, w.Close()
}

func (b *GCSBackend) Fetch(path string) (io.ReadCloser, error) {
	ctx := context.Background()
	obj := b.handle.Object(path)

	// Probe whether the file exists before trying to fetch it
	_, err := obj.Attrs(ctx)
	if err != nil {
		return nil, err
	}

	return obj.NewReader(ctx)
}

// renameObject renames an object in the specified Cloud Storage
// bucket.
//
// The Go API for Cloud Storage does not support renaming objects, but
// the HTTP API does. The code below makes the relevant call manually.
func (b *GCSBackend) Move(old, new string) error {
	ctx := context.Background()
	creds, err := google.FindDefaultCredentials(ctx, gcsScope)
	if err != nil {
		return err
	}

	token, err := creds.TokenSource.Token()
	if err != nil {
		return err
	}

	// as per https://cloud.google.com/storage/docs/renaming-copying-moving-objects#rename
	url := fmt.Sprintf(
		"https://www.googleapis.com/storage/v1/b/%s/o/%s/rewriteTo/b/%s/o/%s",
		url.PathEscape(b.bucket), url.PathEscape(old),
		url.PathEscape(b.bucket), url.PathEscape(new),
	)

	req, err := http.NewRequest("POST", url, nil)
	req.Header.Add("Authorization", "Bearer "+token.AccessToken)
	_, err = client.Do(req)
	if err != nil {
		return err
	}

	// It seems that 'rewriteTo' copies objects instead of
	// renaming/moving them, hence a deletion call afterwards is
	// required.
	if err = b.handle.Object(old).Delete(ctx); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"new": new,
			"old": old,
		}).Warn("failed to delete renamed object")

		// this error should not break renaming and is not returned
	}

	return nil
}

func (b *GCSBackend) ServeLayer(digest string, w http.ResponseWriter) error {
	url, err := b.constructLayerUrl(digest)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"layer":  digest,
			"bucket": b.bucket,
		}).Error("failed to sign GCS URL")

		return err
	}

	w.Header().Set("Location", url)
	w.WriteHeader(303)
	return nil
}

// Configure GCS URL signing in the presence of a service account key
// (toggled if the user has set GOOGLE_APPLICATION_CREDENTIALS).
func signingOptsFromEnv() (*storage.SignedURLOptions, error) {
	path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if path == "" {
		// No credentials configured -> no URL signing
		return nil, nil
	}

	key, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read service account key: %s", err)
	}

	conf, err := google.JWTConfigFromJSON(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse service account key: %s", err)
	}

	log.WithField("account", conf.Email).Info("GCS URL signing enabled")

	return &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		GoogleAccessID: conf.Email,
		PrivateKey:     conf.PrivateKey,
		Method:         "GET",
	}, nil
}

// layerRedirect constructs the public URL of the layer object in the Cloud
// Storage bucket, signs it and redirects the user there.
//
// Signing the URL allows unauthenticated clients to retrieve objects from the
// bucket.
//
// The Docker client is known to follow redirects, but this might not be true
// for all other registry clients.
func (b *GCSBackend) constructLayerUrl(digest string) (string, error) {
	log.WithField("layer", digest).Info("redirecting layer request to bucket")
	object := "layers/" + digest

	if b.signing != nil {
		opts := *b.signing
		opts.Expires = time.Now().Add(5 * time.Minute)
		return storage.SignedURL(b.bucket, object, &opts)
	} else {
		return ("https://storage.googleapis.com/" + b.bucket + "/" + object), nil
	}
}
