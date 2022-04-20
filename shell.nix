# Copyright 2022 The TVL Contributors
# SPDX-License-Identifier: Apache-2.0

# Configures a shell environment that builds required local packages to
# run Nixery.
{ pkgs ? import <nixpkgs> { } }:

let nixery = import ./default.nix { inherit pkgs; };
in pkgs.stdenv.mkDerivation {
  name = "nixery-dev-shell";

  buildInputs = with pkgs; [ jq nixery.nixery-prepare-image ];
}
