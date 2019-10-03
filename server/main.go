// Copyright 2019 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not
// use this file except in compliance with the License. You may obtain a copy of
// the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations under
// the License.

// The nixery server implements a container registry that transparently builds
// container images based on Nix derivations.
//
// The Nix derivation used for image creation is responsible for creating
// objects that are compatible with the registry API. The targeted registry
// protocol is currently Docker's.
//
// When an image is requested, the required contents are parsed out of the
// request and a Nix-build is initiated that eventually responds with the
// manifest as well as information linking each layer digest to a local
// filesystem path.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/nixery/builder"
	"github.com/google/nixery/config"
)

// ManifestMediaType is the Content-Type used for the manifest itself. This
// corresponds to the "Image Manifest V2, Schema 2" described on this page:
//
// https://docs.docker.com/registry/spec/manifest-v2-2/
const manifestMediaType string = "application/vnd.docker.distribution.manifest.v2+json"

// Regexes matching the V2 Registry API routes. This only includes the
// routes required for serving images, since pushing and other such
// functionality is not available.
var (
	manifestRegex = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/manifests/([\w|\-|\.|\_]+)$`)
	layerRegex    = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/blobs/sha256:(\w+)$`)
)

// layerRedirect constructs the public URL of the layer object in the Cloud
// Storage bucket, signs it and redirects the user there.
//
// Signing the URL allows unauthenticated clients to retrieve objects from the
// bucket.
//
// The Docker client is known to follow redirects, but this might not be true
// for all other registry clients.
func constructLayerUrl(cfg *config.Config, digest string) (string, error) {
	log.Printf("Redirecting layer '%s' request to bucket '%s'\n", digest, cfg.Bucket)
	object := "layers/" + digest

	if cfg.Signing != nil {
		opts := *cfg.Signing
		opts.Expires = time.Now().Add(5 * time.Minute)
		return storage.SignedURL(cfg.Bucket, object, &opts)
	} else {
		return ("https://storage.googleapis.com/" + cfg.Bucket + "/" + object), nil
	}
}

// prepareBucket configures the handle to a Cloud Storage bucket in which
// individual layers will be stored after Nix builds. Nixery does not directly
// serve layers to registry clients, instead it redirects them to the public
// URLs of the Cloud Storage bucket.
//
// The bucket is required for Nixery to function correctly, hence fatal errors
// are generated in case it fails to be set up correctly.
func prepareBucket(ctx context.Context, cfg *config.Config) *storage.BucketHandle {
	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatalln("Failed to set up Cloud Storage client:", err)
	}

	bkt := client.Bucket(cfg.Bucket)

	if _, err := bkt.Attrs(ctx); err != nil {
		log.Fatalln("Could not access configured bucket", err)
	}

	return bkt
}

// Error format corresponding to the registry protocol V2 specification. This
// allows feeding back errors to clients in a way that can be presented to
// users.
type registryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type registryErrors struct {
	Errors []registryError `json:"errors"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	err := registryErrors{
		Errors: []registryError{
			{code, message},
		},
	}
	json, _ := json.Marshal(err)

	w.WriteHeader(status)
	w.Header().Add("Content-Type", "application/json")
	w.Write(json)
}

type registryHandler struct {
	ctx   context.Context
	state *builder.State
}

func (h *registryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Acknowledge that we speak V2 with an empty response
	if r.RequestURI == "/v2/" {
		return
	}

	// Serve the manifest (straight from Nix)
	manifestMatches := manifestRegex.FindStringSubmatch(r.RequestURI)
	if len(manifestMatches) == 3 {
		imageName := manifestMatches[1]
		imageTag := manifestMatches[2]
		log.Printf("Requesting manifest for image %q at tag %q", imageName, imageTag)
		image := builder.ImageFromName(imageName, imageTag)
		buildResult, err := builder.BuildImage(h.ctx, h.state, &image)

		if err != nil {
			writeError(w, 500, "UNKNOWN", "image build failure")
			log.Println("Failed to build image manifest", err)
			return
		}

		// Some error types have special handling, which is applied
		// here.
		if buildResult.Error == "not_found" {
			s := fmt.Sprintf("Could not find Nix packages: %v", buildResult.Pkgs)
			writeError(w, 404, "MANIFEST_UNKNOWN", s)
			log.Println(s)
			return
		}

		// This marshaling error is ignored because we know that this
		// field represents valid JSON data.
		manifest, _ := json.Marshal(buildResult.Manifest)
		w.Header().Add("Content-Type", manifestMediaType)
		w.Write(manifest)
		return
	}

	// Serve an image layer. For this we need to first ask Nix for
	// the manifest, then proceed to extract the correct layer from
	// it.
	layerMatches := layerRegex.FindStringSubmatch(r.RequestURI)
	if len(layerMatches) == 3 {
		digest := layerMatches[2]
		url, err := constructLayerUrl(&h.state.Cfg, digest)

		if err != nil {
			log.Printf("Failed to sign GCS URL: %s\n", err)
			writeError(w, 500, "UNKNOWN", "could not serve layer")
			return
		}

		w.Header().Set("Location", url)
		w.WriteHeader(303)
		return
	}

	log.Printf("Unsupported registry route: %s\n", r.RequestURI)
	w.WriteHeader(404)
}

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalln("Failed to load configuration", err)
	}

	ctx := context.Background()
	bucket := prepareBucket(ctx, &cfg)
	cache, err := builder.NewCache()
	if err != nil {
		log.Fatalln("Failed to instantiate build cache", err)
	}

	state := builder.State{
		Bucket: bucket,
		Cache:  &cache,
		Cfg:    cfg,
	}

	log.Printf("Starting Nixery on port %s\n", cfg.Port)

	// All /v2/ requests belong to the registry handler.
	http.Handle("/v2/", &registryHandler{
		ctx:   ctx,
		state: &state,
	})

	// All other roots are served by the static file server.
	webDir := http.Dir(cfg.WebDir)
	http.Handle("/", http.FileServer(webDir))

	log.Fatal(http.ListenAndServe(":"+cfg.Port, nil))
}
