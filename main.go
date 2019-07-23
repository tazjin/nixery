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
//
// Nixery caches the filesystem paths and returns the manifest to the client.
// Subsequent requests for layer content per digest are then fulfilled by
// serving the files from disk.
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

	"cloud.google.com/go/storage"
)

// ManifestMediaType stores the Content-Type used for the manifest itself. This
// corresponds to the "Image Manifest V2, Schema 2" described on this page:
//
// https://docs.docker.com/registry/spec/manifest-v2-2/
const ManifestMediaType string = "application/vnd.docker.distribution.manifest.v2+json"

// Image represents the information necessary for building a container image. This can
// be either a list of package names (corresponding to keys in the nixpkgs set) or a
// Nix expression that results in a *list* of derivations.
type image struct {
	// Name of the container image.
	name string

	// Names of packages to include in the image. These must correspond directly to
	// top-level names of Nix packages in the nixpkgs tree.
	packages []string
}

// BuildResult represents the output of calling the Nix derivation responsible for building
// registry images.
//
// The `layerLocations` field contains the local filesystem paths to each individual image layer
// that will need to be served, while the `manifest` field contains the JSON-representation of
// the manifest that needs to be served to the client.
//
// The later field is simply treated as opaque JSON and passed through.
type BuildResult struct {
	Manifest       json.RawMessage `json:"manifest"`
	LayerLocations map[string]struct {
		Path string `json:"path"`
		Md5  []byte `json:"md5"`
	} `json:"layerLocations"`
}

// imageFromName parses an image name into the corresponding structure which can
// be used to invoke Nix.
//
// It will expand convenience names under the hood (see the `convenienceNames` function below).
func imageFromName(name string) image {
	packages := strings.Split(name, "/")
	return image{
		name:     name,
		packages: convenienceNames(packages),
	}
}

// convenienceNames expands convenience package names defined by Nixery which let users
// include commonly required sets of tools in a container quickly.
//
// Convenience names must be specified as the first package in an image.
//
// Currently defined convenience names are:
//
// * `shell`: Includes bash, coreutils and other common command-line tools
// * `builder`: Includes the standard build environment, as well as everything from `shell`
func convenienceNames(packages []string) []string {
	shellPackages := []string{"bashInteractive", "coreutils", "moreutils", "nano"}
	builderPackages := append(shellPackages, "stdenv")

	if packages[0] == "shell" {
		return append(packages[1:], shellPackages...)
	} else if packages[0] == "builder" {
		return append(packages[1:], builderPackages...)
	} else {
		return packages
	}
}

// Call out to Nix and request that an image be built. Nix will, upon success, return
// a manifest for the container image.
func buildImage(image *image, ctx *context.Context, bucket *storage.BucketHandle) ([]byte, error) {
	// This file is made available at runtime via Blaze. See the `data` declaration in `BUILD`
	nixPath := "experimental/users/tazjin/nixery/build-registry-image.nix"

	packages, err := json.Marshal(image.packages)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("nix-build", "--no-out-link", "--show-trace", "--argstr", "name", image.name, "--argstr", "packages", string(packages), nixPath)

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
	log.Printf("Started Nix image build for ''%s'", image.name)

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

	// The build output returned by Nix is deserialised to add all contained layers to the
	// bucket. Only the manifest itself is re-serialised to JSON and returned.
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

	return json.Marshal(result.Manifest)
}

// uploadLayer uploads a single layer to Cloud Storage bucket. Before writing any data
// the bucket is probed to see if the file already exists.
//
// If the file does exist, its MD5 hash is verified to ensure that the stored file is
// not - for example - a fragment of a previous, incomplete upload.
func uploadLayer(ctx *context.Context, bucket *storage.BucketHandle, layer string, path string, md5 []byte) error {
	layerKey := fmt.Sprintf("layers/%s", layer)
	obj := bucket.Object(layerKey)

	// Before uploading a layer to the bucket, probe whether it already exists.
	//
	// If it does and the MD5 checksum matches the expected one, the layer upload
	// can be skipped.
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

// layerRedirect constructs the public URL of the layer object in the Cloud Storage bucket
// and redirects the client there.
//
// The Docker client is known to follow redirects, but this might not be true for all other
// registry clients.
func layerRedirect(w http.ResponseWriter, bucket string, digest string) {
	log.Printf("Redirecting layer '%s' request to bucket '%s'\n", digest, bucket)
	url := fmt.Sprintf("https://storage.googleapis.com/%s/layers/%s", bucket, digest)
	w.Header().Set("Location", url)
	w.WriteHeader(303)
}

// prepareBucket configures the handle to a Cloud Storage bucket in which individual layers will be
// stored after Nix builds. Nixery does not directly serve layers to registry clients, instead it
// redirects them to the public URLs of the Cloud Storage bucket.
//
// The bucket is required for Nixery to function correctly, hence fatal errors are generated in case
// it fails to be set up correctly.
func prepareBucket(ctx *context.Context, bucket string) *storage.BucketHandle {
	client, err := storage.NewClient(*ctx)
	if err != nil {
		log.Fatalln("Failed to set up Cloud Storage client:", err)
	}

	bkt := client.Bucket(bucket)

	if _, err := bkt.Attrs(*ctx); err != nil {
		log.Fatalln("Could not access configured bucket", err)
	}

	return bkt
}

var manifestRegex = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/manifests/(\w+)$`)
var layerRegex = regexp.MustCompile(`^/v2/([\w|\-|\.|\_|\/]+)/blobs/sha256:(\w+)$`)

func main() {
	bucketName := os.Getenv("BUCKET")
	if bucketName == "" {
		log.Fatalln("GCS bucket for layer storage must be specified")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "5726"
	}

	ctx := context.Background()
	bucket := prepareBucket(&ctx, bucketName)

	log.Printf("Starting Kubernetes Nix controller on port %s\n", port)

	log.Fatal(http.ListenAndServe(":"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When running on AppEngine, HTTP traffic should be redirected to HTTPS.
		//
		// This is achieved here by enforcing HSTS (with a one week duration) on responses.
		if r.Header.Get("X-Forwarded-Proto") == "http" && strings.Contains(r.Host, "appspot.com") {
			w.Header().Add("Strict-Transport-Security", "max-age=604800")
		}

		// Serve an index page to anyone who visits the registry's base URL:
		if r.RequestURI == "/" {
			index, _ := ioutil.ReadFile("experimental/users/tazjin/nixery/index.html")
			w.Header().Add("Content-Type", "text/html")
			w.Write(index)
			return
		}

		// Acknowledge that we speak V2
		if r.RequestURI == "/v2/" {
			fmt.Fprintln(w)
			return
		}

		// Serve the manifest (straight from Nix)
		manifestMatches := manifestRegex.FindStringSubmatch(r.RequestURI)
		if len(manifestMatches) == 3 {
			imageName := manifestMatches[1]
			log.Printf("Requesting manifest for image '%s'", imageName)
			image := imageFromName(manifestMatches[1])
			manifest, err := buildImage(&image, &ctx, bucket)

			if err != nil {
				log.Println("Failed to build image manifest", err)
				return
			}

			w.Header().Add("Content-Type", ManifestMediaType)
			w.Write(manifest)
			return
		}

		// Serve an image layer. For this we need to first ask Nix for the
		// manifest, then proceed to extract the correct layer from it.
		layerMatches := layerRegex.FindStringSubmatch(r.RequestURI)
		if len(layerMatches) == 3 {
			digest := layerMatches[2]
			layerRedirect(w, bucketName, digest)
			return
		}

		w.WriteHeader(404)
	})))
}
