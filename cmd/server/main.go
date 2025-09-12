// Copyright The TVL Contributors
// SPDX-License-Identifier: Apache-2.0

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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"text/template"

	"github.com/google/nixery/assets"
	"github.com/google/nixery/builder"
	"github.com/google/nixery/config"
	"github.com/google/nixery/layers"
	mf "github.com/google/nixery/manifest"
	"github.com/google/nixery/storage"
	"github.com/im7mortal/kmutex"
)

// ManifestMediaType is the Content-Type used for the manifest itself. This
// corresponds to the "Image Manifest V2, Schema 2" described on this page:
//
// https://docs.docker.com/registry/spec/manifest-v2-2/
const manifestMediaType string = "application/vnd.docker.distribution.manifest.v2+json"

// This variable will be initialised during the build process and set
// to the hash of the entire Nixery source tree.
var version string = "devel"

// indexHandler serves the main page with dynamic hostname replacement
type indexHandler struct {
	template *template.Template
	errors   *builder.ErrorCache
}

func (h *indexHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hostname := r.Host
	if hostname == "" {
		hostname = "nixery.dev"
	}

	data := struct {
		Hostname string
		Version  string
		Errors   []*builder.BuildError
	}{
		Hostname: hostname,
		Version:  version,
		Errors:   h.errors.GetAllErrors(),
	}

	err := h.template.Execute(w, data)
	if err != nil {
		slog.Error("failed to execute template", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// Regexes matching the V2 Registry API routes. This only includes the
// routes required for serving images, since pushing and other such
// functionality is not available.
var (
	manifestRegex = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/manifests/([\w|\-|\.|\_]+)$`)
	blobRegex     = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/(blobs|manifests)/sha256:(\w+)$`)
)

// Downloads the popularity information for the package set from the
// URL specified in Nixery's configuration.
func downloadPopularity(url string) (layers.Popularity, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("popularity download from '%s' returned status: %s\n", url, resp.Status)
	}

	j, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var pop layers.Popularity
	err = json.Unmarshal(j, &pop)
	if err != nil {
		return nil, err
	}

	return pop, nil
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
	state *builder.State
}

// Serve a manifest by tag, building it via Nix and populating caches
// if necessary.
func (h *registryHandler) serveManifestTag(w http.ResponseWriter, r *http.Request, name string, tag string) {
	slog.Info("requesting image manifest", "image", name, "tag", tag)

	image := builder.ImageFromName(name, tag)
	buildResult, err := builder.BuildImage(r.Context(), h.state, &image)

	if err != nil {
		writeError(w, 500, "UNKNOWN", "image build failure")

		slog.Error("failed to build image manifest", "err", err, "image", name, "tag", tag)

		return
	}

	// Some error types have special handling, which is applied
	// here.
	if buildResult.Error == "not_found" {
		s := fmt.Sprintf("Could not find Nix packages: %v", buildResult.Pkgs)
		writeError(w, 404, "MANIFEST_UNKNOWN", s)

		slog.Warn("could not find Nix packages", "image", name, "tag", tag, "packages", buildResult.Pkgs)

		return
	}

	// This marshaling error is ignored because we know that this
	// field represents valid JSON data.
	manifest, _ := json.Marshal(buildResult.Manifest)
	w.Header().Add("Content-Type", manifestMediaType)

	// The manifest needs to be persisted to the blob storage (to become
	// available for clients that fetch manifests by their hash, e.g.
	// containerd) and served to the client.
	//
	// Since we have no stable key to address this manifest (it may be
	// uncacheable, yet still addressable by blob) we need to separate
	// out the hashing, uploading and serving phases. The latter is
	// especially important as clients may start to fetch it by digest
	// as soon as they see a response.
	sha256sum := fmt.Sprintf("%x", sha256.Sum256(manifest))
	path := "layers/" + sha256sum
	ctx := r.Context()

	_, _, err = h.state.Storage.Persist(ctx, path, mf.ManifestType, func(sw io.Writer) (string, int64, error) {
		// We already know the hash, so no additional hash needs to be
		// constructed here.
		written, err := sw.Write(manifest)
		return sha256sum, int64(written), err
	})

	if err != nil {
		writeError(w, 500, "MANIFEST_UPLOAD", "could not upload manifest to blob store")

		slog.Error("could not upload manifest", "err", err, "image", name, "tag", tag)

		return
	}

	w.Write(manifest)
}

// serveBlob serves a blob from storage by digest
func (h *registryHandler) serveBlob(w http.ResponseWriter, r *http.Request, blobType, digest string) {
	storage := h.state.Storage
	err := storage.Serve(digest, r, w)
	if err != nil {
		slog.Error("failed to serve blob from storage backend", "err", err, "type", blobType, "digest", digest, "backend", storage.Name())
	}
}

// ServeHTTP dispatches HTTP requests to the matching handlers.
func (h *registryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Acknowledge that we speak V2 with an empty response
	if r.RequestURI == "/v2/" {
		return
	}

	// Build & serve a manifest by tag
	manifestMatches := manifestRegex.FindStringSubmatch(r.RequestURI)
	if len(manifestMatches) == 3 {
		h.serveManifestTag(w, r, manifestMatches[1], manifestMatches[2])
		return
	}

	// Serve a blob by digest
	layerMatches := blobRegex.FindStringSubmatch(r.RequestURI)
	if len(layerMatches) == 4 {
		h.serveBlob(w, r, layerMatches[2], layerMatches[3])
		return
	}

	slog.Info("unsupported registry route", "uri", r.RequestURI)

	w.WriteHeader(404)
}

func main() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	slog.Info("initialised logging", "service", "nixery", "version", version)
	cfg, err := config.FromEnv()
	if err != nil {
		slog.Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	var s storage.Backend

	switch cfg.Backend {
	case config.GCS:
		s, err = storage.NewGCSBackend()
	case config.FileSystem:
		s, err = storage.NewFSBackend()
	}
	if err != nil {
		slog.Error("failed to initialise storage backend", "err", err)
		os.Exit(1)
	}

	slog.Info("initialised storage backend", "backend", s.Name())

	cache, err := builder.NewCache()
	if err != nil {
		slog.Error("failed to instantiate build cache", "err", err)
		os.Exit(1)
	}

	var pop layers.Popularity
	if cfg.PopUrl != "" {
		pop, err = downloadPopularity(cfg.PopUrl)
		if err != nil {
			slog.Error("failed to fetch popularity information", "err", err, "popURL", cfg.PopUrl)
			os.Exit(1)
		}
	}

	state := builder.State{
		Cache:       &cache,
		Cfg:         cfg,
		Pop:         pop,
		Storage:     s,
		UploadMutex: kmutex.New(),
		Errors:      builder.NewErrorCache(15),
	}

	slog.Info("starting Nixery", "version", version, "port", cfg.Port)

	// All /v2/ requests belong to the registry handler.
	http.Handle("/v2/", &registryHandler{
		state: &state,
	})

	// Parse the embedded index template
	tmpl, err := template.New("index").Parse(assets.IndexTemplate)
	if err != nil {
		slog.Error("failed to parse index template", "err", err)
		os.Exit(1)
	}

	// Serve the main index page with dynamic content
	http.Handle("/", &indexHandler{tmpl, state.Errors})

	// Serve static assets (logo, etc.) from embedded filesystem
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets.Files))))

	err = http.ListenAndServe(":"+cfg.Port, nil)
	if err != nil {
		slog.Error("HTTP server error", "err", err)
		os.Exit(1)
	}
}
