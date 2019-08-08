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

// Package main provides the implementation of a container registry that
// transparently builds container images based on Nix derivations.
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/storage"
)

// pkgSource represents the source from which the Nix package set used
// by Nixery is imported. Users configure the source by setting one of
// the supported environment variables.
type pkgSource struct {
	srcType string
	args    string
}

// Convert the package source into the representation required by Nix.
func (p *pkgSource) renderSource(tag string) string {
	// The 'git' source requires a tag to be present.
	if p.srcType == "git" {
		if tag == "latest" || tag == "" {
			tag = "master"
		}

		return fmt.Sprintf("git!%s!%s", p.args, tag)
	}

	return fmt.Sprintf("%s!%s", p.srcType, p.args)
}

// Retrieve a package source from the environment. If no source is
// specified, the Nix code will default to a recent NixOS channel.
func pkgSourceFromEnv() *pkgSource {
	if channel := os.Getenv("NIXERY_CHANNEL"); channel != "" {
		log.Printf("Using Nix package set from Nix channel %q\n", channel)
		return &pkgSource{
			srcType: "nixpkgs",
			args:    channel,
		}
	}

	if git := os.Getenv("NIXERY_PKGS_REPO"); git != "" {
		log.Printf("Using Nix package set from git repository at %q\n", git)
		return &pkgSource{
			srcType: "git",
			args:    git,
		}
	}

	if path := os.Getenv("NIXERY_PKGS_PATH"); path != "" {
		log.Printf("Using Nix package set from path %q\n", path)
		return &pkgSource{
			srcType: "path",
			args:    path,
		}
	}

	return nil
}

// Load (optional) GCS bucket signing data from the GCS_SIGNING_KEY and
// GCS_SIGNING_ACCOUNT envvars.
func signingOptsFromEnv() *storage.SignedURLOptions {
	path := os.Getenv("GCS_SIGNING_KEY")
	id := os.Getenv("GCS_SIGNING_ACCOUNT")

	if path == "" || id == "" {
		log.Println("GCS URL signing disabled")
		return nil
	}

	log.Printf("GCS URL signing enabled with account %q\n", id)
	k, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalf("Failed to read GCS signing key: %s\n", err)
	}

	return &storage.SignedURLOptions{
		GoogleAccessID: id,
		PrivateKey:     k,
		Method:         "GET",
	}
}

// config holds the Nixery configuration options.
type config struct {
	bucket  string                    // GCS bucket to cache & serve layers
	signing *storage.SignedURLOptions // Signing options to use for GCS URLs
	builder string                    // Nix derivation for building images
	port    string                    // Port on which to launch HTTP server
	pkgs    *pkgSource                // Source for Nix package set
}

// ManifestMediaType is the Content-Type used for the manifest itself. This
// corresponds to the "Image Manifest V2, Schema 2" described on this page:
//
// https://docs.docker.com/registry/spec/manifest-v2-2/
const manifestMediaType string = "application/vnd.docker.distribution.manifest.v2+json"

// Image represents the information necessary for building a container image.
// This can be either a list of package names (corresponding to keys in the
// nixpkgs set) or a Nix expression that results in a *list* of derivations.
type image struct {
	name string
	tag string

	// Names of packages to include in the image. These must correspond
	// directly to top-level names of Nix packages in the nixpkgs tree.
	packages []string
}

// BuildResult represents the output of calling the Nix derivation responsible
// for building registry images.
//
// The `layerLocations` field contains the local filesystem paths to each
// individual image layer that will need to be served, while the `manifest`
// field contains the JSON-representation of the manifest that needs to be
// served to the client.
//
// The later field is simply treated as opaque JSON and passed through.
type BuildResult struct {
	Error string   `json:"error"`
	Pkgs  []string `json:"pkgs"`

	Manifest       json.RawMessage `json:"manifest"`
	LayerLocations map[string]struct {
		Path string `json:"path"`
		Md5  []byte `json:"md5"`
	} `json:"layerLocations"`
}

// imageFromName parses an image name into the corresponding structure which can
// be used to invoke Nix.
//
// It will expand convenience names under the hood (see the `convenienceNames`
// function below).
func imageFromName(name string, tag string) image {
	packages := strings.Split(name, "/")
	return image{
		name:     name,
		tag:      tag,
		packages: convenienceNames(packages),
	}
}

// convenienceNames expands convenience package names defined by Nixery which
// let users include commonly required sets of tools in a container quickly.
//
// Convenience names must be specified as the first package in an image.
//
// Currently defined convenience names are:
//
// * `shell`: Includes bash, coreutils and other common command-line tools
// * `builder`: All of the above and the standard build environment
func convenienceNames(packages []string) []string {
	shellPackages := []string{"bashInteractive", "coreutils", "moreutils", "nano"}

	if packages[0] == "shell" {
		return append(packages[1:], shellPackages...)
	}

	return packages
}

// Call out to Nix and request that an image be built. Nix will, upon success,
// return a manifest for the container image.
func buildImage(ctx *context.Context, cfg *config, image *image, bucket *storage.BucketHandle) (*BuildResult, error) {
	packages, err := json.Marshal(image.packages)
	if err != nil {
		return nil, err
	}

	args := []string{
		"--no-out-link",
		"--show-trace",
		"--argstr", "name", image.name,
		"--argstr", "packages", string(packages), cfg.builder,
	}

	if cfg.pkgs != nil {
		args = append(args, "--argstr", "pkgSource", cfg.pkgs.renderSource(image.tag))
	}
	cmd := exec.Command("nix-build", args...)

	outpipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	errpipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err = cmd.Start(); err != nil {
		log.Println("Error starting nix-build:", err)
		return nil, err
	}
	log.Printf("Started Nix image build for '%s'", image.name)

	stdout, _ := ioutil.ReadAll(outpipe)
	stderr, _ := ioutil.ReadAll(errpipe)

	if err = cmd.Wait(); err != nil {
		// TODO(tazjin): Propagate errors upwards in a usable format.
		log.Printf("nix-build execution error: %s\nstdout: %s\nstderr: %s\n", err, stdout, stderr)
		return nil, err
	}

	log.Println("Finished Nix image build")

	buildOutput, err := ioutil.ReadFile(strings.TrimSpace(string(stdout)))
	if err != nil {
		return nil, err
	}

	// The build output returned by Nix is deserialised to add all
	// contained layers to the bucket. Only the manifest itself is
	// re-serialised to JSON and returned.
	var result BuildResult
	err = json.Unmarshal(buildOutput, &result)
	if err != nil {
		return nil, err
	}

	for layer, meta := range result.LayerLocations {
		err = uploadLayer(ctx, bucket, layer, meta.Path, meta.Md5)
		if err != nil {
			return nil, err
		}
	}

	return &result, nil
}

// uploadLayer uploads a single layer to Cloud Storage bucket. Before writing
// any data the bucket is probed to see if the file already exists.
//
// If the file does exist, its MD5 hash is verified to ensure that the stored
// file is not - for example - a fragment of a previous, incomplete upload.
func uploadLayer(ctx *context.Context, bucket *storage.BucketHandle, layer string, path string, md5 []byte) error {
	layerKey := fmt.Sprintf("layers/%s", layer)
	obj := bucket.Object(layerKey)

	// Before uploading a layer to the bucket, probe whether it already
	// exists.
	//
	// If it does and the MD5 checksum matches the expected one, the layer
	// upload can be skipped.
	attrs, err := obj.Attrs(*ctx)

	if err == nil && bytes.Equal(attrs.MD5, md5) {
		log.Printf("Layer sha256:%s already exists in bucket, skipping upload", layer)
	} else {
		writer := obj.NewWriter(*ctx)
		file, err := os.Open(path)

		if err != nil {
			return fmt.Errorf("failed to open layer %s from path %s: %v", layer, path, err)
		}

		size, err := io.Copy(writer, file)
		if err != nil {
			return fmt.Errorf("failed to write layer %s to Cloud Storage: %v", layer, err)
		}

		if err = writer.Close(); err != nil {
			return fmt.Errorf("failed to write layer %s to Cloud Storage: %v", layer, err)
		}

		log.Printf("Uploaded layer sha256:%s (%v bytes written)\n", layer, size)
	}

	return nil
}

// layerRedirect constructs the public URL of the layer object in the Cloud
// Storage bucket, signs it and redirects the user there.
//
// Signing the URL allows unauthenticated clients to retrieve objects from the
// bucket.
//
// The Docker client is known to follow redirects, but this might not be true
// for all other registry clients.
func constructLayerUrl(cfg *config, digest string) (string, error) {
	log.Printf("Redirecting layer '%s' request to bucket '%s'\n", digest, cfg.bucket)
	object := "layers/" + digest

	if cfg.signing != nil {
		opts := *cfg.signing
		opts.Expires = time.Now().Add(5 * time.Minute)
		return storage.SignedURL(cfg.bucket, object, &opts)
	} else {
		return ("https://storage.googleapis.com/" + cfg.bucket + "/" + object), nil
	}
}

// prepareBucket configures the handle to a Cloud Storage bucket in which
// individual layers will be stored after Nix builds. Nixery does not directly
// serve layers to registry clients, instead it redirects them to the public
// URLs of the Cloud Storage bucket.
//
// The bucket is required for Nixery to function correctly, hence fatal errors
// are generated in case it fails to be set up correctly.
func prepareBucket(ctx *context.Context, cfg *config) *storage.BucketHandle {
	client, err := storage.NewClient(*ctx)
	if err != nil {
		log.Fatalln("Failed to set up Cloud Storage client:", err)
	}

	bkt := client.Bucket(cfg.bucket)

	if _, err := bkt.Attrs(*ctx); err != nil {
		log.Fatalln("Could not access configured bucket", err)
	}

	return bkt
}

// Regexes matching the V2 Registry API routes. This only includes the
// routes required for serving images, since pushing and other such
// functionality is not available.
var (
	manifestRegex = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/manifests/([\w|\-|\.|\_]+)$`)
	layerRegex    = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/blobs/sha256:(\w+)$`)
)

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
	cfg    *config
	ctx    *context.Context
	bucket *storage.BucketHandle
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
		image := imageFromName(imageName, imageTag)
		buildResult, err := buildImage(h.ctx, h.cfg, &image, h.bucket)

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
		url, err := constructLayerUrl(h.cfg, digest)

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

func getConfig(key, desc string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalln(desc + " must be specified")
	}

	return value
}

func main() {
	cfg := &config{
		bucket:  getConfig("BUCKET", "GCS bucket for layer storage"),
		builder: getConfig("NIX_BUILDER", "Nix image builder code"),
		port:    getConfig("PORT", "HTTP port"),
		pkgs:    pkgSourceFromEnv(),
		signing: signingOptsFromEnv(),
	}

	ctx := context.Background()
	bucket := prepareBucket(&ctx, cfg)

	log.Printf("Starting Kubernetes Nix controller on port %s\n", cfg.port)

	// All /v2/ requests belong to the registry handler.
	http.Handle("/v2/", &registryHandler{
		cfg:    cfg,
		ctx:    &ctx,
		bucket: bucket,
	})

	// All other roots are served by the static file server.
	webDir := http.Dir(getConfig("WEB_DIR", "Static web file dir"))
	http.Handle("/", http.FileServer(webDir))

	log.Fatal(http.ListenAndServe(":"+cfg.port, nil))
}
