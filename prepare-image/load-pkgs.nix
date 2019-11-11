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

# Load a Nix package set from one of the supported source types
# (nixpkgs, git, path).
{ srcType, srcArgs, importArgs ? { } }:

with builtins;
let
  # If a nixpkgs channel is requested, it is retrieved from Github (as
  # a tarball) and imported.
  fetchImportChannel = channel:
    let
      url =
        "https://github.com/NixOS/nixpkgs-channels/archive/${channel}.tar.gz";
    in import (fetchTarball url) importArgs;

  # If a git repository is requested, it is retrieved via
  # builtins.fetchGit which defaults to the git configuration of the
  # outside environment. This means that user-configured SSH
  # credentials etc. are going to work as expected.
  fetchImportGit = spec: import (fetchGit spec) importArgs;

  # No special handling is used for paths, so users are expected to pass one
  # that will work natively with Nix.
  importPath = path: import (toPath path) importArgs;
in if srcType == "nixpkgs" then
  fetchImportChannel srcArgs
else if srcType == "git" then
  fetchImportGit (fromJSON srcArgs)
else if srcType == "path" then
  importPath srcArgs
else
  throw ("Invalid package set source specification: ${srcType} (${srcArgs})")
