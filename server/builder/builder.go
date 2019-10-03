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
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/google/nixery/layers"
	"github.com/google/nixery/manifest"
	"golang.org/x/oauth2/google"
)

// The maximum number of layers in an image is 125. To allow for
// extensibility, the actual number of layers Nixery is "allowed" to
// use up is set at a lower point.
const LayerBudget int = 94

// API scope needed for renaming objects in GCS
const gcsScope = "https://www.googleapis.com/auth/devstorage.read_write"

// HTTP client to use for direct calls to APIs that are not part of the SDK
var client = &http.Client{}

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

// BuildResult represents the data returned from the server to the
// HTTP handlers. Error information is propagated straight from Nix
// for errors inside of the build that should be fed back to the
// client (such as missing packages).
type BuildResult struct {
	Error    string          `json:"error"`
	Pkgs     []string        `json:"pkgs"`
	Manifest json.RawMessage `json:"manifest"`
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

// ImageResult represents the output of calling the Nix derivation
// responsible for preparing an image.
type ImageResult struct {
	// These fields are populated in case of an error
	Error string   `json:"error"`
	Pkgs  []string `json:"pkgs"`

	// These fields are populated in case of success
	Graph        layers.RuntimeGraph `json:"runtimeGraph"`
	SymlinkLayer struct {
		Size   int    `json:"size"`
		SHA256 string `json:"sha256"`
		MD5    string `json:"md5"`
		Path   string `json:"path"`
	} `json:"symlinkLayer"`
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

func callNix(program string, name string, args []string) ([]byte, error) {
	cmd := exec.Command(program, args...)

	outpipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	errpipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	go logNix(name, errpipe)

	if err = cmd.Start(); err != nil {
		log.Printf("Error starting %s: %s\n", program, err)
		return nil, err
	}
	log.Printf("Invoked Nix build (%s) for '%s'\n", program, name)

	stdout, _ := ioutil.ReadAll(outpipe)

	if err = cmd.Wait(); err != nil {
		log.Printf("%s execution error: %s\nstdout: %s\n", program, err, stdout)
		return nil, err
	}

	resultFile := strings.TrimSpace(string(stdout))
	buildOutput, err := ioutil.ReadFile(resultFile)
	if err != nil {
		return nil, err
	}

	return buildOutput, nil
}

// Call out to Nix and request metadata for the image to be built. All
// required store paths for the image will be realised, but layers
// will not yet be created from them.
//
// This function is only invoked if the manifest is not found in any
// cache.
func prepareImage(s *State, image *Image) (*ImageResult, error) {
	packages, err := json.Marshal(image.Packages)
	if err != nil {
		return nil, err
	}

	srcType, srcArgs := s.Cfg.Pkgs.Render(image.Tag)

	args := []string{
		"--timeout", s.Cfg.Timeout,
		"--argstr", "packages", string(packages),
		"--argstr", "srcType", srcType,
		"--argstr", "srcArgs", srcArgs,
	}

	output, err := callNix("nixery-build-image", image.Name, args)
	if err != nil {
		log.Printf("failed to call nixery-build-image: %s\n", err)
		return nil, err
	}
	log.Printf("Finished image preparation for '%s' via Nix\n", image.Name)

	var result ImageResult
	err = json.Unmarshal(output, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// Groups layers and checks whether they are present in the cache
// already, otherwise calls out to Nix to assemble layers.
//
// Returns information about all data layers that need to be included
// in the manifest, as well as information about which layers need to
// be uploaded (and from where).
func prepareLayers(ctx context.Context, s *State, image *Image, graph *layers.RuntimeGraph) (map[string]string, error) {
	grouped := layers.Group(graph, &s.Pop, LayerBudget)

	// TODO(tazjin): Introduce caching strategy, for now this will
	// build all layers.
	srcType, srcArgs := s.Cfg.Pkgs.Render(image.Tag)
	args := []string{
		"--argstr", "srcType", srcType,
		"--argstr", "srcArgs", srcArgs,
	}

	layerInput := make(map[string][]string)
	allPaths := []string{}
	for _, l := range grouped {
		layerInput[l.Hash()] = l.Contents

		// The derivation responsible for building layers does not
		// have the derivations that resulted in the required store
		// paths in its context, which means that its sandbox will not
		// contain the necessary paths if sandboxing is enabled.
		//
		// To work around this, all required store paths are added as
		// 'extra-sandbox-paths' parameters.
		for _, p := range l.Contents {
			allPaths = append(allPaths, p)
		}
	}

	args = append(args, "--option", "extra-sandbox-paths", strings.Join(allPaths, " "))

	j, _ := json.Marshal(layerInput)
	args = append(args, "--argstr", "layers", string(j))

	output, err := callNix("nixery-build-layers", image.Name, args)
	if err != nil {
		log.Printf("failed to call nixery-build-layers: %s\n", err)
		return nil, err
	}
	log.Printf("Finished layer preparation for '%s' via Nix\n", image.Name)

	result := make(map[string]string)
	err = json.Unmarshal(output, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// renameObject renames an object in the specified Cloud Storage
// bucket.
//
// The Go API for Cloud Storage does not support renaming objects, but
// the HTTP API does. The code below makes the relevant call manually.
func renameObject(ctx context.Context, s *State, old, new string) error {
	bucket := s.Cfg.Bucket

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
		url.PathEscape(bucket), url.PathEscape(old),
		url.PathEscape(bucket), url.PathEscape(new),
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
	if err = s.Bucket.Object(old).Delete(ctx); err != nil {
		log.Printf("failed to delete renamed object '%s': %s\n", old, err)
		// this error should not break renaming and is not returned
	}

	return nil
}

// Upload a to the storage bucket, while hashing it at the same time.
//
// The initial upload is performed in a 'staging' folder, as the
// SHA256-hash is not yet available when the upload is initiated.
//
// After a successful upload, the file is moved to its final location
// in the bucket and the build cache is populated.
//
// The return value is the layer's SHA256 hash, which is used in the
// image manifest.
func uploadHashLayer(ctx context.Context, s *State, key string, data io.Reader) (*manifest.Entry, error) {
	staging := s.Bucket.Object("staging/" + key)

	// Sets up a "multiwriter" that simultaneously runs both hash
	// algorithms and uploads to the bucket
	sw := staging.NewWriter(ctx)
	shasum := sha256.New()
	md5sum := md5.New()
	multi := io.MultiWriter(sw, shasum, md5sum)

	size, err := io.Copy(multi, data)
	if err != nil {
		log.Printf("failed to upload layer '%s' to staging: %s\n", key, err)
		return nil, err
	}

	if err = sw.Close(); err != nil {
		log.Printf("failed to upload layer '%s' to staging: %s\n", key, err)
		return nil, err
	}

	build := Build{
		SHA256: fmt.Sprintf("%x", shasum.Sum([]byte{})),
		MD5:    fmt.Sprintf("%x", md5sum.Sum([]byte{})),
	}

	// Hashes are now known and the object is in the bucket, what
	// remains is to move it to the correct location and cache it.
	err = renameObject(ctx, s, "staging/"+key, "layers/"+build.SHA256)
	if err != nil {
		log.Printf("failed to move layer '%s' from staging: %s\n", key, err)
		return nil, err
	}

	cacheBuild(ctx, s, key, build)

	log.Printf("Uploaded layer sha256:%s (%v bytes written)", build.SHA256, size)

	return &manifest.Entry{
		Digest: "sha256:" + build.SHA256,
		Size:   size,
	}, nil
}

func BuildImage(ctx context.Context, s *State, image *Image) (*BuildResult, error) {
	key := s.Cfg.Pkgs.CacheKey(image.Packages, image.Tag)
	if key != "" {
		if m, c := manifestFromCache(ctx, s, key); c {
			return &BuildResult{
				Manifest: m,
			}, nil
		}
	}

	imageResult, err := prepareImage(s, image)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare image '%s': %s", image.Name, err)
	}

	if imageResult.Error != "" {
		return &BuildResult{
			Error: imageResult.Error,
			Pkgs:  imageResult.Pkgs,
		}, nil
	}

	layerResult, err := prepareLayers(ctx, s, image, &imageResult.Graph)
	if err != nil {
		return nil, err
	}

	layerResult[imageResult.SymlinkLayer.SHA256] = imageResult.SymlinkLayer.Path

	layers := []manifest.Entry{}
	for key, path := range layerResult {
		f, err := os.Open(path)
		if err != nil {
			log.Printf("failed to open layer at '%s': %s\n", path, err)
			return nil, err
		}

		entry, err := uploadHashLayer(ctx, s, key, f)
		f.Close()
		if err != nil {
			return nil, err
		}

		layers = append(layers, *entry)
	}

	m, c := manifest.Manifest(layers)
	if _, err = uploadHashLayer(ctx, s, c.SHA256, bytes.NewReader(c.Config)); err != nil {
		log.Printf("failed to upload config for %s: %s\n", image.Name, err)
		return nil, err
	}

	if key != "" {
		go cacheManifest(ctx, s, key, m)
	}

	result := BuildResult{
		Manifest: m,
	}
	return &result, nil
}
