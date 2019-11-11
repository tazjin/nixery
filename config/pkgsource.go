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
package config

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
)

// PkgSource represents the source from which the Nix package set used
// by Nixery is imported. Users configure the source by setting one of
// the supported environment variables.
type PkgSource interface {
	// Convert the package source into the representation required
	// for calling Nix.
	Render(tag string) (string, string)

	// Create a key by which builds for this source and iamge
	// combination can be cached.
	//
	// The empty string means that this value is not cacheable due
	// to the package source being a moving target (such as a
	// channel).
	CacheKey(pkgs []string, tag string) string
}

type GitSource struct {
	repository string
}

// Regex to determine whether a git reference is a commit hash or
// something else (branch/tag).
//
// Used to check whether a git reference is cacheable, and to pass the
// correct git structure to Nix.
//
// Note: If a user creates a branch or tag with the name of a commit
// and references it intentionally, this heuristic will fail.
var commitRegex = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (g *GitSource) Render(tag string) (string, string) {
	args := map[string]string{
		"url": g.repository,
	}

	// The 'git' source requires a tag to be present. If the user
	// has not specified one, it is assumed that the default
	// 'master' branch should be used.
	if tag == "latest" || tag == "" {
		tag = "master"
	}

	if commitRegex.MatchString(tag) {
		args["rev"] = tag
	} else {
		args["ref"] = tag
	}

	j, _ := json.Marshal(args)

	return "git", string(j)
}

func (g *GitSource) CacheKey(pkgs []string, tag string) string {
	// Only full commit hashes can be used for caching, as
	// everything else is potentially a moving target.
	if !commitRegex.MatchString(tag) {
		return ""
	}

	unhashed := strings.Join(pkgs, "") + tag
	hashed := fmt.Sprintf("%x", sha1.Sum([]byte(unhashed)))

	return hashed
}

type NixChannel struct {
	channel string
}

func (n *NixChannel) Render(tag string) (string, string) {
	return "nixpkgs", n.channel
}

func (n *NixChannel) CacheKey(pkgs []string, tag string) string {
	// Since Nix channels are downloaded from the nixpkgs-channels
	// Github, users can specify full commit hashes as the
	// "channel", in which case builds are cacheable.
	if !commitRegex.MatchString(n.channel) {
		return ""
	}

	unhashed := strings.Join(pkgs, "") + n.channel
	hashed := fmt.Sprintf("%x", sha1.Sum([]byte(unhashed)))

	return hashed
}

type PkgsPath struct {
	path string
}

func (p *PkgsPath) Render(tag string) (string, string) {
	return "path", p.path
}

func (p *PkgsPath) CacheKey(pkgs []string, tag string) string {
	// Path-based builds are not currently cacheable because we
	// have no local hash of the package folder's state easily
	// available.
	return ""
}

// Retrieve a package source from the environment. If no source is
// specified, the Nix code will default to a recent NixOS channel.
func pkgSourceFromEnv() (PkgSource, error) {
	if channel := os.Getenv("NIXERY_CHANNEL"); channel != "" {
		log.WithField("channel", channel).Info("using Nix package set from Nix channel or commit")

		return &NixChannel{
			channel: channel,
		}, nil
	}

	if git := os.Getenv("NIXERY_PKGS_REPO"); git != "" {
		log.WithField("repo", git).Info("using NIx package set from git repository")

		return &GitSource{
			repository: git,
		}, nil
	}

	if path := os.Getenv("NIXERY_PKGS_PATH"); path != "" {
		log.WithField("path", path).Info("using Nix package set at local path")

		return &PkgsPath{
			path: path,
		}, nil
	}

	return nil, fmt.Errorf("no valid package source has been specified")
}
