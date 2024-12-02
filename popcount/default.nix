# Copyright 2022, 2024 The TVL Contributors
# SPDX-License-Identifier: Apache-2.0

{ buildGoModule }:

buildGoModule {
  name = "nixery-popcount";

  src = ./.;

  vendorHash = null;

  # https://nixos.org/manual/nixpkgs/stable/#buildGoPackage-migration
  postPatch = ''
    go mod init github.com/google/nixery/popcount
  '';

  doCheck = true;
}
