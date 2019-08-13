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

# Load a Nix package set from a source specified in one of the following
# formats:
#
# 1. nixpkgs!$channel (e.g. nixpkgs!nixos-19.03)
# 2. git!$repo!$rev (e.g. git!git@github.com:NixOS/nixpkgs.git!master)
# 3. path!$path (e.g. path!/var/local/nixpkgs)
#
# '!' was chosen as the separator because `builtins.split` does not
# support regex escapes and there are few other candidates. It
# doesn't matter much because this is invoked by the server.
{ pkgSource, args ? { } }:

with builtins;
let
  # If a nixpkgs channel is requested, it is retrieved from Github (as
  # a tarball) and imported.
  fetchImportChannel = channel:
    let
      url =
        "https://github.com/NixOS/nixpkgs-channels/archive/${channel}.tar.gz";
    in import (fetchTarball url) args;

  # If a git repository is requested, it is retrieved via
  # builtins.fetchGit which defaults to the git configuration of the
  # outside environment. This means that user-configured SSH
  # credentials etc. are going to work as expected.
  fetchImportGit = url: rev:
    let
      # builtins.fetchGit needs to know whether 'rev' is a reference
      # (e.g. a branch/tag) or a revision (i.e. a commit hash)
      #
      # Since this data is being extrapolated from the supplied image
      # tag, we have to guess if we want to avoid specifying a format.
      #
      # There are some additional caveats around whether the default
      # branch contains the specified revision, which need to be
      # explained to users.
      spec = if (stringLength rev) == 40 then {
        inherit url rev;
      } else {
        inherit url;
        ref = rev;
      };
    in import (fetchGit spec) args;

  # No special handling is used for paths, so users are expected to pass one
  # that will work natively with Nix.
  importPath = path: import (toPath path) args;

  source = split "!" pkgSource;
  sourceType = elemAt source 0;
in if sourceType == "nixpkgs" then
  fetchImportChannel (elemAt source 2)
else if sourceType == "git" then
  fetchImportGit (elemAt source 2) (elemAt source 4)
else if sourceType == "path" then
  importPath (elemAt source 2)
else
  throw ("Invalid package set source specification: ${pkgSource}")
