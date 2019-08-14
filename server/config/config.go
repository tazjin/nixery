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

// Package config implements structures to store Nixery's configuration at
// runtime as well as the logic for instantiating this configuration from the
// environment.
package config

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"cloud.google.com/go/storage"
)

// pkgSource represents the source from which the Nix package set used
// by Nixery is imported. Users configure the source by setting one of
// the supported environment variables.
type PkgSource struct {
	srcType string
	args    string
}

// Convert the package source into the representation required by Nix.
func (p *PkgSource) Render(tag string) string {
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
func pkgSourceFromEnv() *PkgSource {
	if channel := os.Getenv("NIXERY_CHANNEL"); channel != "" {
		log.Printf("Using Nix package set from Nix channel %q\n", channel)
		return &PkgSource{
			srcType: "nixpkgs",
			args:    channel,
		}
	}

	if git := os.Getenv("NIXERY_PKGS_REPO"); git != "" {
		log.Printf("Using Nix package set from git repository at %q\n", git)
		return &PkgSource{
			srcType: "git",
			args:    git,
		}
	}

	if path := os.Getenv("NIXERY_PKGS_PATH"); path != "" {
		log.Printf("Using Nix package set from path %q\n", path)
		return &PkgSource{
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

func getConfig(key, desc string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalln(desc + " must be specified")
	}

	return value
}

// config holds the Nixery configuration options.
type Config struct {
	Bucket  string                    // GCS bucket to cache & serve layers
	Signing *storage.SignedURLOptions // Signing options to use for GCS URLs
	Port    string                    // Port on which to launch HTTP server
	Pkgs    *PkgSource                // Source for Nix package set
	WebDir  string
}

func FromEnv() *Config {
	return &Config{
		Bucket:  getConfig("BUCKET", "GCS bucket for layer storage"),
		Port:    getConfig("PORT", "HTTP port"),
		Pkgs:    pkgSourceFromEnv(),
		Signing: signingOptsFromEnv(),
		WebDir:  getConfig("WEB_DIR", "Static web file dir"),
	}
}
