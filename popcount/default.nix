# Copyright 2022 The TVL Contributors
# SPDX-License-Identifier: Apache-2.0

{ buildGoPackage }:

buildGoPackage {
  name = "nixery-popcount";

  src = ./.;

  goPackagePath = "github.com/google/nixery/popcount";
  doCheck = true;
}
