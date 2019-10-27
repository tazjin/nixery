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
	"os"

	log "github.com/sirupsen/logrus"
)

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
	Port    string    // Port on which to launch HTTP server
	Pkgs    PkgSource // Source for Nix package set
	Timeout string    // Timeout for a single Nix builder (seconds)
	WebDir  string    // Directory with static web assets
	PopUrl  string    // URL to the Nix package popularity count
}

func FromEnv() (Config, error) {
	pkgs, err := pkgSourceFromEnv()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Port:    getConfig("PORT", "HTTP port", ""),
		Pkgs:    pkgs,
		Timeout: getConfig("NIX_TIMEOUT", "Nix builder timeout", "60"),
		WebDir:  getConfig("WEB_DIR", "Static web file dir", ""),
		PopUrl:  os.Getenv("NIX_POPULARITY_URL"),
	}, nil
}
