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

// Package builder implements the code required to build images via Nix. Image
// build data is cached for up to 24 hours to avoid duplicated calls to Nix
// (which are costly even if no building is performed).
package builder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/google/nixery/config"
)

// Image represents the information necessary for building a container image.
// This can be either a list of package names (corresponding to keys in the
// nixpkgs set) or a Nix expression that results in a *list* of derivations.
type Image struct {
	Name string
	Tag  string

	// Names of packages to include in the image. These must correspond
	// directly to top-level names of Nix packages in the nixpkgs tree.
	Packages []string
}

// ImageFromName parses an image name into the corresponding structure which can
// be used to invoke Nix.
//
// It will expand convenience names under the hood (see the `convenienceNames`
// function below).
//
// Once assembled the image structure uses a sorted representation of
// the name. This is to avoid unnecessarily cache-busting images if
// only the order of requested packages has changed.
func ImageFromName(name string, tag string) Image {
	pkgs := strings.Split(name, "/")
	expanded := convenienceNames(pkgs)

	sort.Strings(pkgs)
	sort.Strings(expanded)

	return Image{
		Name:     strings.Join(pkgs, "/"),
		Tag:      tag,
		Packages: expanded,
	}
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
	Error    string          `json:"error"`
	Pkgs     []string        `json:"pkgs"`
	Manifest json.RawMessage `json:"manifest"`

	LayerLocations map[string]struct {
		Path string `json:"path"`
		Md5  []byte `json:"md5"`
	} `json:"layerLocations"`
}

// convenienceNames expands convenience package names defined by Nixery which
// let users include commonly required sets of tools in a container quickly.
//
// Convenience names must be specified as the first package in an image.
//
// Currently defined convenience names are:
//
// * `shell`: Includes bash, coreutils and other common command-line tools
func convenienceNames(packages []string) []string {
	shellPackages := []string{"bashInteractive", "cacert", "coreutils", "iana-etc", "moreutils", "nano"}

	if packages[0] == "shell" {
		return append(packages[1:], shellPackages...)
	}

	return packages
}

// logNix logs each output line from Nix. It runs in a goroutine per
// output channel that should be live-logged.
func logNix(name string, r io.ReadCloser) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		log.Printf("\x1b[31m[nix - %s]\x1b[39m %s\n", name, scanner.Text())
	}
}

// Call out to Nix and request that an image be built. Nix will, upon success,
// return a manifest for the container image.
func BuildImage(ctx *context.Context, cfg *config.Config, cache *LocalCache, image *Image, bucket *storage.BucketHandle) (*BuildResult, error) {
	var resultFile string
	cached := false

	key := cfg.Pkgs.CacheKey(image.Packages, image.Tag)
	if key != "" {
		resultFile, cached = manifestFromCache(ctx, cache, bucket, key)
	}

	if !cached {
		packages, err := json.Marshal(image.Packages)
		if err != nil {
			return nil, err
		}

		srcType, srcArgs := cfg.Pkgs.Render(image.Tag)

		args := []string{
			"--timeout", cfg.Timeout,
			"--argstr", "name", image.Name,
			"--argstr", "packages", string(packages),
			"--argstr", "srcType", srcType,
			"--argstr", "srcArgs", srcArgs,
		}

		if cfg.PopUrl != "" {
			args = append(args, "--argstr", "popularityUrl", cfg.PopUrl)
		}

		cmd := exec.Command("nixery-build-image", args...)

		outpipe, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}

		errpipe, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
		go logNix(image.Name, errpipe)

		if err = cmd.Start(); err != nil {
			log.Println("Error starting nix-build:", err)
			return nil, err
		}
		log.Printf("Started Nix image build for '%s'", image.Name)

		stdout, _ := ioutil.ReadAll(outpipe)

		if err = cmd.Wait(); err != nil {
			log.Printf("nix-build execution error: %s\nstdout: %s\n", err, stdout)
			return nil, err
		}

		log.Println("Finished Nix image build")

		resultFile = strings.TrimSpace(string(stdout))

		if key != "" {
			cacheManifest(ctx, cache, bucket, key, resultFile)
		}
	}

	buildOutput, err := ioutil.ReadFile(resultFile)
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
		if !cache.hasSeenLayer(layer) {
			err = uploadLayer(ctx, bucket, layer, meta.Path, meta.Md5)
			if err != nil {
				return nil, err
			}

			cache.sawLayer(layer)
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
