// Package image implements logic for creating the image metadata
// (such as the image manifest and configuration).
package manifest

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

const (
	// manifest constants
	schemaVersion = 2

	// media types
	manifestType = "application/vnd.docker.distribution.manifest.v2+json"
	layerType    = "application/vnd.docker.image.rootfs.diff.tar"
	configType   = "application/vnd.docker.container.image.v1+json"

	// image config constants
	arch   = "amd64"
	os     = "linux"
	fsType = "layers"
)

type Entry struct {
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
}

type manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	MediaType     string  `json:"mediaType"`
	Config        Entry   `json:"config"`
	Layers        []Entry `json:"layers"`
}

type imageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`

	RootFS struct {
		FSType  string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`

	// sic! empty struct (rather than `null`) is required by the
	// image metadata deserialiser in Kubernetes
	Config struct{} `json:"config"`
}

// ConfigLayer represents the configuration layer to be included in
// the manifest, containing its JSON-serialised content and the SHA256
// & MD5 hashes of its input.
type ConfigLayer struct {
	Config []byte
	SHA256 string
	MD5    string
}

// imageConfig creates an image configuration with the values set to
// the constant defaults.
//
// Outside of this module the image configuration is treated as an
// opaque blob and it is thus returned as an already serialised byte
// array and its SHA256-hash.
func configLayer(hashes []string) ConfigLayer {
	c := imageConfig{}
	c.Architecture = arch
	c.OS = os
	c.RootFS.FSType = fsType
	c.RootFS.DiffIDs = hashes

	j, _ := json.Marshal(c)

	return ConfigLayer{
		Config: j,
		SHA256: fmt.Sprintf("%x", sha256.Sum256(j)),
		MD5:    fmt.Sprintf("%x", md5.Sum(j)),
	}
}

// Manifest creates an image manifest from the specified layer entries
// and returns its JSON-serialised form as well as the configuration
// layer.
//
// Callers do not need to set the media type for the layer entries.
func Manifest(layers []Entry) (json.RawMessage, ConfigLayer) {
	hashes := make([]string, len(layers))
	for i, l := range layers {
		l.MediaType = "application/vnd.docker.image.rootfs.diff.tar"
		layers[i] = l
		hashes[i] = l.Digest
	}

	c := configLayer(hashes)

	m := manifest{
		SchemaVersion: schemaVersion,
		MediaType:     manifestType,
		Config: Entry{
			MediaType: configType,
			Size:      int64(len(c.Config)),
			Digest:    "sha256:" + c.SHA256,
		},
		Layers: layers,
	}

	j, _ := json.Marshal(m)

	return json.RawMessage(j), c
}
