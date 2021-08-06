# Copyright 2019-2021 Google LLC
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

{ pkgs ? import ./nixpkgs-pin.nix
, preLaunch ? ""
, extraPackages ? []
, maxLayers ? 20
, commitHash ? null }@args:

with pkgs;

let
  inherit (pkgs) buildGoModule;

  # Current Nixery commit - this is used as the Nixery version in
  # builds to distinguish errors between deployed versions, see
  # server/logs.go for details.
  nixery-commit-hash = args.commitHash or pkgs.lib.commitIdFromGitRepo ./.git;

  # Go implementation of the Nixery server which implements the
  # container registry interface.
  #
  # Users should use the nixery-bin derivation below instead as it
  # provides the paths of files needed at runtime.
  nixery-server = buildGoModule rec {
    name = "nixery-server";
    src = ./.;
    doCheck = true;

    # Needs to be updated after every modification of go.mod/go.sum
    vendorSha256 = "1adjav0dxb97ws0w2k50rhk6r46wvfry6aj4sik3ninl525kd15s";

    buildFlagsArray = [
      "-ldflags=-s -w -X main.version=${nixery-commit-hash}"
    ];
  };
in rec {
  # Implementation of the Nix image building logic
  nixery-prepare-image = import ./prepare-image { inherit pkgs; };

  # Use mdBook to build a static asset page which Nixery can then
  # serve. This is primarily used for the public instance at
  # nixery.dev.
  nixery-book = callPackage ./docs { };

  # Wrapper script running the Nixery server with the above two data
  # dependencies configured.
  #
  # In most cases, this will be the derivation a user wants if they
  # are installing Nixery directly.
  nixery-bin = writeShellScriptBin "nixery" ''
    export WEB_DIR="${nixery-book}"
    export PATH="${nixery-prepare-image}/bin:$PATH"
    exec ${nixery-server}/bin/nixery
  '';

  nixery-popcount = callPackage ./popcount { };

  # Container image containing Nixery and Nix itself. This image can
  # be run on Kubernetes, published on AppEngine or whatever else is
  # desired.
  nixery-image = let
    # Wrapper script for the wrapper script (meta!) which configures
    # the container environment appropriately.
    #
    # Most importantly, sandboxing is disabled to avoid privilege
    # issues in containers.
    nixery-launch-script = writeShellScriptBin "nixery" ''
      set -e
      export PATH=${coreutils}/bin:$PATH
      export NIX_SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt
      mkdir -p /tmp

      # Create the build user/group required by Nix
      echo 'nixbld:x:30000:nixbld' >> /etc/group
      echo 'nixbld:x:30000:30000:nixbld:/tmp:/bin/bash' >> /etc/passwd
      echo 'root:x:0:0:root:/root:/bin/bash' >> /etc/passwd
      echo 'root:x:0:' >> /etc/group

      # Disable sandboxing to avoid running into privilege issues
      mkdir -p /etc/nix
      echo 'sandbox = false' >> /etc/nix/nix.conf

      # In some cases users building their own image might want to
      # customise something on the inside (e.g. set up an environment
      # for keys or whatever).
      #
      # This can be achieved by setting a 'preLaunch' script.
      ${preLaunch}

      exec ${nixery-bin}/bin/nixery
    '';
  in dockerTools.buildLayeredImage {
    name = "nixery";
    config.Cmd = [ "${nixery-launch-script}/bin/nixery" ];

    inherit maxLayers;
    contents = [
      bashInteractive
      cacert
      coreutils
      git
      gnutar
      gzip
      iana-etc
      nix
      nixery-prepare-image
      nixery-launch-script
      openssh
      zlib
    ] ++ extraPackages;
  };
}
