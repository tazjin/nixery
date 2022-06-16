# Copyright 2022 The TVL Contributors
# SPDX-License-Identifier: Apache-2.0

# Builds the documentation page using the Rust project's 'mdBook'
# tool.
#
# Some of the documentation is pulled in and included from other
# sources.

{ fetchFromGitHub, mdbook, runCommand, rustPlatform, nix-1p, postamble ? "" }:

runCommand "nixery-book"
{
  POSTAMBLE = postamble;
} ''
  mkdir -p $out
  cp -r ${./.}/* .
  chmod -R a+w src
  cp ${nix-1p}/README.md src/nix-1p.md
  echo "''${POSTAMBLE}" >> src/nixery.md
  ${mdbook}/bin/mdbook build -d $out
''
