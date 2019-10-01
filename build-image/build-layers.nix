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

{
  # Description of the package set to be used (will be loaded by load-pkgs.nix)
  srcType ? "nixpkgs",
  srcArgs ? "nixos-19.03",
  importArgs ? { },
  # Path to load-pkgs.nix
  loadPkgs ? ./load-pkgs.nix,
  # Layers to assemble into tarballs
  layers ? "{}"
}:

let
  inherit (builtins) fromJSON mapAttrs toJSON;
  inherit (pkgs) lib runCommand writeText;

  pkgs = import loadPkgs { inherit srcType srcArgs importArgs; };

  # Given a list of store paths, create an image layer tarball with
  # their contents.
  pathsToLayer = paths: runCommand "layer.tar" {
  } ''
    tar --no-recursion -Prf "$out" \
        --mtime="@$SOURCE_DATE_EPOCH" \
        --owner=0 --group=0 /nix /nix/store

    tar -Prpf "$out" --hard-dereference --sort=name \
        --mtime="@$SOURCE_DATE_EPOCH" \
        --owner=0 --group=0 ${lib.concatStringsSep " " paths}
  '';


  layerTarballs = mapAttrs (_: pathsToLayer ) (fromJSON layers);
in writeText "layer-tarballs.json" (toJSON layerTarballs)
