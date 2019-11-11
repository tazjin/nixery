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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"

	"github.com/google/nixery/builder"
	"github.com/google/nixery/config"
	"github.com/google/nixery/logs"
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
	layerRegex    = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/blobs/sha256:(\w+)$`)
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

		log.WithFields(log.Fields{
			"image": imageName,
			"tag":   imageTag,
		}).Info("requesting image manifest")

		image := builder.ImageFromName(imageName, imageTag)
		buildResult, err := builder.BuildImage(r.Context(), h.state, &image)

		if err != nil {
			writeError(w, 500, "UNKNOWN", "image build failure")

			log.WithError(err).WithFields(log.Fields{
				"image": imageName,
				"tag":   imageTag,
			}).Error("failed to build image manifest")

			return
		}

		// Some error types have special handling, which is applied
		// here.
		if buildResult.Error == "not_found" {
			s := fmt.Sprintf("Could not find Nix packages: %v", buildResult.Pkgs)
			writeError(w, 404, "MANIFEST_UNKNOWN", s)

			log.WithFields(log.Fields{
				"image":    imageName,
				"tag":      imageTag,
				"packages": buildResult.Pkgs,
			}).Warn("could not find Nix packages")

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
		storage := h.state.Storage
		err := storage.ServeLayer(digest, r, w)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"layer":   digest,
				"backend": storage.Name(),
			}).Error("failed to serve layer from storage backend")
		}

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
