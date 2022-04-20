# Copyright 2022 The TVL Contributors
# SPDX-License-Identifier: Apache-2.0

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
        "https://github.com/NixOS/nixpkgs/archive/${channel}.tar.gz";
    in
    import (fetchTarball url) importArgs;

  # If a git repository is requested, it is retrieved via
  # builtins.fetchGit which defaults to the git configuration of the
  # outside environment. This means that user-configured SSH
  # credentials etc. are going to work as expected.
  fetchImportGit = spec: import (fetchGit spec) importArgs;

  # No special handling is used for paths, so users are expected to pass one
  # that will work natively with Nix.
  importPath = path: import (toPath path) importArgs;
in
if srcType == "nixpkgs" then
  fetchImportChannel srcArgs
else if srcType == "git" then
  fetchImportGit (fromJSON srcArgs)
else if srcType == "path" then
  importPath srcArgs
else
  throw ("Invalid package set source specification: ${srcType} (${srcArgs})")
