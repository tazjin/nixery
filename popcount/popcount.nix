# Copyright 2019 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script, given a target attribute in `nixpkgs`, builds the
# target derivations' runtime closure and returns its reference graph.
#
# This is invoked by popcount.sh for each package in nixpkgs to
# collect all package references, so that package popularity can be
# tracked.
#
# Check out build-image/group-layers.go for an in-depth explanation of
# what the popularity counts are used for.

{ pkgs ? import <nixpkgs> { config.allowUnfree = false; }, target }:

let
  inherit (pkgs) coreutils runCommand writeText;
  inherit (builtins) readFile toFile fromJSON toJSON listToAttrs;

  # graphJSON abuses feature in Nix that makes structured runtime
  # closure information available to builders. This data is imported
  # back via IFD to process it for layering data.
  graphJSON = path:
    runCommand "build-graph" {
      __structuredAttrs = true;
      exportReferencesGraph.graph = path;
      PATH = "${coreutils}/bin";
      builder = toFile "builder" ''
        . .attrs.sh
        cat .attrs.json > ''${outputs[out]}
      '';
    } "";

  buildClosures = paths: (fromJSON (readFile (graphJSON paths)));

  buildGraph = paths:
    listToAttrs (map (c: {
      name = c.path;
      value = { inherit (c) closureSize references; };
    }) (buildClosures paths));
in writeText "${target}-graph"
(toJSON (buildClosures [ pkgs."${target}" ]).graph)
