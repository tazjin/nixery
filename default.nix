# Copyright 2022 The TVL Contributors
# SPDX-License-Identifier: Apache-2.0

# This function header aims to provide compatibility between builds of
# Nixery taking place inside/outside of the TVL depot.
#
# In the future, Nixery will transition to using //nix/buildGo for its
# build system and this will need some major adaptations to support
# that.
{ depot ? { nix.readTree.drvTargets = x: x; }
, pkgs ? import <nixpkgs> { }
, preLaunch ? ""
, extraPackages ? [ ]
, maxLayers ? 20
, commitHash ? null
, ...
}@args:

with pkgs;

let
  inherit (pkgs) buildGoModule lib;

  # Avoid extracting this from git until we have a way to plumb
  # through revision numbers.
  nixery-commit-hash = "depot";
in
depot.nix.readTree.drvTargets rec {
  # Implementation of the Nix image building logic
  nixery-prepare-image = import ./prepare-image { inherit pkgs; };

  # Include the Nixery website into the Nix store, unless its being
  # overridden to something else. Nixery will serve this as its front
  # page when visited from a browser.
  nixery-web = ./web;

  nixery-popcount = callPackage ./popcount { };

  # Build Nixery's Go code, resulting in the binaries used for various
  # bits of functionality.
  #
  # The server binary is wrapped to ensure that required environment
  # variables are set at runtime.
  nixery = buildGoModule rec {
    name = "nixery";
    src = ./.;
    doCheck = true;

    # Needs to be updated after every modification of go.mod/go.sum
    vendorHash = "sha256-io9NCeZmjCZPLmII3ajXIsBWbT40XiW8ncXOuUDabbo=";

    ldflags = [
      "-s"
      "-w"
      "-X"
      "main.version=${nixery-commit-hash}"
    ];

    nativeBuildInputs = [ makeWrapper ];
    postInstall = ''
      wrapProgram $out/bin/server \
        --set-default WEB_DIR "${nixery-web}" \
        --prefix PATH : ${nixery-prepare-image}/bin
    '';

    # Nixery is mirrored to Github at tazjin/nixery; this is
    # automatically updated from CI for canon builds.
    passthru.meta.ci.extraSteps.github = depot.tools.releases.filteredGitPush {
      filter = ":/tools/nixery";
      remote = "git@github.com:tazjin/nixery.git";
      ref = "refs/heads/master";
    };
  };

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

    exec ${nixery}/bin/server
  '';

  # Container image containing Nixery and Nix itself. This image can
  # be run on Kubernetes, published on AppEngine or whatever else is
  # desired.
  nixery-image = dockerTools.buildLayeredImage {
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
