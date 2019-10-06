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
	"io/ioutil"
	"os"

	"cloud.google.com/go/storage"
	log "github.com/sirupsen/logrus"
)

// Load (optional) GCS bucket signing data from the GCS_SIGNING_KEY and
// GCS_SIGNING_ACCOUNT envvars.
func signingOptsFromEnv() *storage.SignedURLOptions {
	path := os.Getenv("GCS_SIGNING_KEY")
	id := os.Getenv("GCS_SIGNING_ACCOUNT")

	if path == "" || id == "" {
		log.Info("GCS URL signing disabled")
		return nil
	}

	log.WithField("account", id).Info("GCS URL signing enabled")

	k, err := ioutil.ReadFile(path)
	if err != nil {
		log.WithError(err).WithField("file", path).Fatal("failed to read GCS signing key")
	}

	return &storage.SignedURLOptions{
		GoogleAccessID: id,
		PrivateKey:     k,
		Method:         "GET",
	}
}

func getConfig(key, desc, def string) string {
	value := os.Getenv(key)
	if value == "" && def == "" {
		log.WithFields(log.Fields{
			"option":      key,
			"description": desc,
		}).Fatal("missing required configuration envvar")
	} else if value == "" {
		return def
	}

	return value
}

// Config holds the Nixery configuration options.
type Config struct {
	Bucket  string                    // GCS bucket to cache & serve layers
	Signing *storage.SignedURLOptions // Signing options to use for GCS URLs
	Port    string                    // Port on which to launch HTTP server
	Pkgs    PkgSource                 // Source for Nix package set
	Timeout string                    // Timeout for a single Nix builder (seconds)
	WebDir  string                    // Directory with static web assets
	PopUrl  string                    // URL to the Nix package popularity count
}

func FromEnv() (Config, error) {
	pkgs, err := pkgSourceFromEnv()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Bucket:  getConfig("BUCKET", "GCS bucket for layer storage", ""),
		Port:    getConfig("PORT", "HTTP port", ""),
		Pkgs:    pkgs,
		Signing: signingOptsFromEnv(),
		Timeout: getConfig("NIX_TIMEOUT", "Nix builder timeout", "60"),
		WebDir:  getConfig("WEB_DIR", "Static web file dir", ""),
		PopUrl:  os.Getenv("NIX_POPULARITY_URL"),
	}, nil
}
