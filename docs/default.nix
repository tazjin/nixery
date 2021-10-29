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

# Builds the documentation page using the Rust project's 'mdBook'
# tool.
#
# Some of the documentation is pulled in and included from other
# sources.

{ fetchFromGitHub, mdbook, runCommand, rustPlatform }:

let
  nix-1p = fetchFromGitHub {
    owner = "tazjin";
    repo = "nix-1p";
    rev = "9f0baf5e270128d9101ba4446cf6844889e399a2";
    sha256 = "1pf9i90gn98vz67h296w5lnwhssk62dc6pij983dff42dbci7lhj";
  };
in runCommand "nixery-book" { } ''
  mkdir -p $out
  cp -r ${./.}/* .
  chmod -R a+w src
  cp ${nix-1p}/README.md src/nix-1p.md
  ${mdbook}/bin/mdbook build -d $out
''
