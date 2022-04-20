# Copyright 2022 The TVL Contributors
# SPDX-License-Identifier: Apache-2.0

# This file builds a wrapper script called by Nixery to ask for the
# content information for a given image.
#
# The purpose of using a wrapper script is to ensure that the paths to
# all required Nix files are set correctly at runtime.

{ pkgs ? import <nixpkgs> { } }:

pkgs.writeShellScriptBin "nixery-prepare-image" ''
  exec ${pkgs.nix}/bin/nix-build \
    --show-trace \
    --no-out-link "$@" \
    --argstr loadPkgs ${./load-pkgs.nix} \
    ${./prepare-image.nix}
''
