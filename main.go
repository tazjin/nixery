// Copyright 2019-2020 Google LLC
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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"regexp"

	"github.com/google/nixery/builder"
	"github.com/google/nixery/config"
	"github.com/google/nixery/logs"
	mf "github.com/google/nixery/manifest"
	"github.com/google/nixery/storage"
	log "github.com/sirupsen/logrus"
)

// ManifestMediaType is the Content-Type used for the manifest itself. This
// corresponds to the "Image Manifest V2, Schema 2" described on this page:
//
// https://docs.docker.com/registry/spec/manifest-v2-2/
const manifestMediaType string = "application/vnd.docker.distribution.manifest.v2+json"

// This variable will be initialised during the build process and set
// to the hash of the entire Nixery source tree.
var version string = "devel"

// Regexes matching the V2 Registry API routes. This only includes the
// routes required for serving images, since pushing and other such
// functionality is not available.
var (
	manifestRegex = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/manifests/([\w|\-|\.|\_]+)$`)
	blobRegex     = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/(blobs|manifests)/sha256:(\w+)$`)
)

// Downloads the popularity information for the package set from the
// URL specified in Nixery's configuration.
func downloadPopularity(url string) (builder.Popularity, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("popularity download from '%s' returned status: %s\n", url, resp.Status)
	}

	j, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var pop builder.Popularity
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
	log.WithFields(log.Fields{
		"image": name,
		"tag":   tag,
	}).Info("requesting image manifest")

	image := builder.ImageFromName(name, tag)
	buildResult, err := builder.BuildImage(r.Context(), h.state, &image)

	if err != nil {
		writeError(w, 500, "UNKNOWN", "image build failure")

		log.WithError(err).WithFields(log.Fields{
			"image": name,
			"tag":   tag,
		}).Error("failed to build image manifest")

		return
	}

	// Some error types have special handling, which is applied
	// here.
	if buildResult.Error == "not_found" {
		s := fmt.Sprintf("Could not find Nix packages: %v", buildResult.Pkgs)
		writeError(w, 404, "MANIFEST_UNKNOWN", s)

		log.WithFields(log.Fields{
			"image":    name,
			"tag":      tag,
			"packages": buildResult.Pkgs,
		}).Warn("could not find Nix packages")

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
	ctx := context.TODO()

	_, _, err = h.state.Storage.Persist(ctx, path, mf.ManifestType, func(sw io.Writer) (string, int64, error) {
		// We already know the hash, so no additional hash needs to be
		// constructed here.
		written, err := sw.Write(manifest)
		return sha256sum, int64(written), err
	})

	if err != nil {
		writeError(w, 500, "MANIFEST_UPLOAD", "could not upload manifest to blob store")

		log.WithError(err).WithFields(log.Fields{
			"image": name,
			"tag":   tag,
		}).Error("could not upload manifest")

		return
	}

	w.Write(manifest)
}

// serveBlob serves a blob from storage by digest
func (h *registryHandler) serveBlob(w http.ResponseWriter, r *http.Request, blobType, digest string) {
	storage := h.state.Storage
	err := storage.Serve(digest, r, w)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"type":    blobType,
			"digest":  digest,
			"backend": storage.Name(),
		}).Error("failed to serve blob from storage backend")
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

	log.WithField("uri", r.RequestURI).Info("unsupported registry route")

	w.WriteHeader(404)
}

func main() {
	logs.Init(version)
	cfg, err := config.FromEnv()
	if err != nil {
		log.WithError(err).Fatal("failed to load configuration")
	}

	var s storage.Backend

	switch cfg.Backend {
	case config.GCS:
		s, err = storage.NewGCSBackend()
	case config.FileSystem:
		s, err = storage.NewFSBackend()
	}
	if err != nil {
		log.WithError(err).Fatal("failed to initialise storage backend")
	}

	log.WithField("backend", s.Name()).Info("initialised storage backend")

	cache, err := builder.NewCache()
	if err != nil {
		log.WithError(err).Fatal("failed to instantiate build cache")
	}

	var pop builder.Popularity
	if cfg.PopUrl != "" {
		pop, err = downloadPopularity(cfg.PopUrl)
		if err != nil {
			log.WithError(err).WithField("popURL", cfg.PopUrl).
				Fatal("failed to fetch popularity information")
		}
	}

	state := builder.State{
		Cache:   &cache,
		Cfg:     cfg,
		Pop:     pop,
		Storage: s,
	}

	log.WithFields(log.Fields{
		"version": version,
		"port":    cfg.Port,
	}).Info("starting Nixery")

	// All /v2/ requests belong to the registry handler.
	http.Handle("/v2/", &registryHandler{
		state: &state,
	})

	// All other roots are served by the static file server.
	webDir := http.Dir(cfg.WebDir)
	http.Handle("/", http.FileServer(webDir))

	log.Fatal(http.ListenAndServe(":"+cfg.Port, nil))
}
