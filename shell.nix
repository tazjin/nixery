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

# Configures a shell environment that builds required local packages to
# run Nixery.
{pkgs ? import <nixpkgs> {} }:

let nixery = import ./default.nix { inherit pkgs; };
in pkgs.stdenv.mkDerivation {
  name = "nixery-dev-shell";

  buildInputs = with pkgs;[
    jq
    nixery.nixery-build-image
  ];
}
