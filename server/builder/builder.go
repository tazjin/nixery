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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/google/nixery/server/config"
	"github.com/google/nixery/server/layers"
	"github.com/google/nixery/server/manifest"
	log "github.com/sirupsen/logrus"
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

// State holds the runtime state that is carried around in Nixery and
// passed to builder functions.
type State struct {
	Bucket *storage.BucketHandle
	Cache  *LocalCache
	Cfg    config.Config
	Pop    layers.Popularity
}

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
// function below) and append packages that are always included (cacert, iana-etc).
//
// Once assembled the image structure uses a sorted representation of
// the name. This is to avoid unnecessarily cache-busting images if
// only the order of requested packages has changed.
func ImageFromName(name string, tag string) Image {
	pkgs := strings.Split(name, "/")
	expanded := convenienceNames(pkgs)
	expanded = append(expanded, "cacert", "iana-etc")

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
		Size     int    `json:"size"`
		TarHash  string `json:"tarHash"`
		GzipHash string `json:"gzipHash"`
		Path     string `json:"path"`
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
	shellPackages := []string{"bashInteractive", "coreutils", "moreutils", "nano"}

	if packages[0] == "shell" {
		return append(packages[1:], shellPackages...)
	}

	return packages
}

// logNix logs each output line from Nix. It runs in a goroutine per
// output channel that should be live-logged.
func logNix(image, cmd string, r io.ReadCloser) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		log.WithFields(log.Fields{
			"image": image,
			"cmd":   cmd,
		}).Info("[nix] " + scanner.Text())
	}
}

func callNix(program, image string, args []string) ([]byte, error) {
	cmd := exec.Command(program, args...)

	outpipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	errpipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	go logNix(program, image, errpipe)

	if err = cmd.Start(); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"image": image,
			"cmd":   program,
		}).Error("error invoking Nix")

		return nil, err
	}

	log.WithFields(log.Fields{
		"cmd":   program,
		"image": image,
	}).Info("invoked Nix build")

	stdout, _ := ioutil.ReadAll(outpipe)

	if err = cmd.Wait(); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"image":  image,
			"cmd":    program,
			"stdout": stdout,
		}).Info("failed to invoke Nix")

		return nil, err
	}

	resultFile := strings.TrimSpace(string(stdout))
	buildOutput, err := ioutil.ReadFile(resultFile)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"image": image,
			"file":  resultFile,
		}).Info("failed to read Nix result file")

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
		// granular error logging is performed in callNix already
		return nil, err
	}

	log.WithFields(log.Fields{
		"image": image.Name,
		"tag":   image.Tag,
	}).Info("finished image preparation via Nix")

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
// Newly built layers are uploaded to the bucket. Cache entries are
// added only after successful uploads, which guarantees that entries
// retrieved from the cache are present in the bucket.
func prepareLayers(ctx context.Context, s *State, image *Image, result *ImageResult) ([]manifest.Entry, error) {
	grouped := layers.Group(&result.Graph, &s.Pop, LayerBudget)

	var entries []manifest.Entry

	// Splits the layers into those which are already present in
	// the cache, and those that are missing.
	//
	// Missing layers are built and uploaded to the storage
	// bucket.
	for _, l := range grouped {
		if entry, cached := layerFromCache(ctx, s, l.Hash()); cached {
			entries = append(entries, *entry)
		} else {
			lh := l.Hash()

			// While packing store paths, the SHA sum of
			// the uncompressed layer is computed and
			// written to `tarhash`.
			//
			// TODO(tazjin): Refactor this to make the
			// flow of data cleaner.
			var tarhash string
			lw := func(w io.Writer) error {
				var err error
				tarhash, err = packStorePaths(&l, w)
				return err
			}

			entry, err := uploadHashLayer(ctx, s, lh, lw)
			if err != nil {
				return nil, err
			}
			entry.MergeRating = l.MergeRating
			entry.TarHash = tarhash

			var pkgs []string
			for _, p := range l.Contents {
				pkgs = append(pkgs, layers.PackageFromPath(p))
			}

			log.WithFields(log.Fields{
				"layer":    lh,
				"packages": pkgs,
				"tarhash":  tarhash,
			}).Info("created image layer")

			go cacheLayer(ctx, s, l.Hash(), *entry)
			entries = append(entries, *entry)
		}
	}

	// Symlink layer (built in the first Nix build) needs to be
	// included here manually:
	slkey := result.SymlinkLayer.GzipHash
	entry, err := uploadHashLayer(ctx, s, slkey, func(w io.Writer) error {
		f, err := os.Open(result.SymlinkLayer.Path)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"image": image.Name,
				"tag":   image.Tag,
				"layer": slkey,
			}).Error("failed to upload symlink layer")

			return err
		}
		defer f.Close()

		_, err = io.Copy(w, f)
		return err
	})

	if err != nil {
		return nil, err
	}

	entry.TarHash = "sha256:" + result.SymlinkLayer.TarHash
	go cacheLayer(ctx, s, slkey, *entry)
	entries = append(entries, *entry)

	return entries, nil
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
		log.WithError(err).WithFields(log.Fields{
			"new": new,
			"old": old,
		}).Warn("failed to delete renamed object")

		// this error should not break renaming and is not returned
	}

	return nil
}

// layerWriter is the type for functions that can write a layer to the
// multiwriter used for uploading & hashing.
//
// This type exists to avoid duplication between the handling of
// symlink layers and store path layers.
type layerWriter func(w io.Writer) error

// byteCounter is a special io.Writer that counts all bytes written to
// it and does nothing else.
//
// This is required because the ad-hoc writing of tarballs leaves no
// single place to count the final tarball size otherwise.
type byteCounter struct {
	count int64
}

func (b *byteCounter) Write(p []byte) (n int, err error) {
	b.count += int64(len(p))
	return len(p), nil
}

// Upload a layer tarball to the storage bucket, while hashing it at
// the same time. The supplied function is expected to provide the
// layer data to the writer.
//
// The initial upload is performed in a 'staging' folder, as the
// SHA256-hash is not yet available when the upload is initiated.
//
// After a successful upload, the file is moved to its final location
// in the bucket and the build cache is populated.
//
// The return value is the layer's SHA256 hash, which is used in the
// image manifest.
func uploadHashLayer(ctx context.Context, s *State, key string, lw layerWriter) (*manifest.Entry, error) {
	staging := s.Bucket.Object("staging/" + key)

	// Sets up a "multiwriter" that simultaneously runs both hash
	// algorithms and uploads to the bucket
	sw := staging.NewWriter(ctx)
	shasum := sha256.New()
	counter := &byteCounter{}
	multi := io.MultiWriter(sw, shasum, counter)

	err := lw(multi)
	if err != nil {
		log.WithError(err).WithField("layer", key).
			Error("failed to create and upload layer")

		return nil, err
	}

	if err = sw.Close(); err != nil {
		log.WithError(err).WithField("layer", key).
			Error("failed to upload layer to staging")
	}

	sha256sum := fmt.Sprintf("%x", shasum.Sum([]byte{}))

	// Hashes are now known and the object is in the bucket, what
	// remains is to move it to the correct location and cache it.
	err = renameObject(ctx, s, "staging/"+key, "layers/"+sha256sum)
	if err != nil {
		log.WithError(err).WithField("layer", key).
			Error("failed to move layer from staging")

		return nil, err
	}

	size := counter.count

	log.WithFields(log.Fields{
		"layer":  key,
		"sha256": sha256sum,
		"size":   size,
	}).Info("uploaded layer")

	entry := manifest.Entry{
		Digest: "sha256:" + sha256sum,
		Size:   size,
	}

	return &entry, nil
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
		return nil, err
	}

	if imageResult.Error != "" {
		return &BuildResult{
			Error: imageResult.Error,
			Pkgs:  imageResult.Pkgs,
		}, nil
	}

	layers, err := prepareLayers(ctx, s, image, imageResult)
	if err != nil {
		return nil, err
	}

	m, c := manifest.Manifest(layers)

	lw := func(w io.Writer) error {
		r := bytes.NewReader(c.Config)
		_, err := io.Copy(w, r)
		return err
	}

	if _, err = uploadHashLayer(ctx, s, c.SHA256, lw); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"image": image.Name,
			"tag":   image.Tag,
		}).Error("failed to upload config")

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
