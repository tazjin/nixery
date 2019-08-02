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
{ pkgs ? import <nixpkgs> {} }:

with pkgs;

rec {
  # Go implementation of the Nixery server which implements the
  # container registry interface.
  #
  # Users will usually not want to use this directly, instead see the
  # 'nixery' derivation below, which automatically includes runtime
  # data dependencies.
  nixery-server = buildGoPackage {
    name = "nixery-server";

    # Technically people should not be building Nixery through 'go get'
    # or similar (as other required files will not be included), but
    # buildGoPackage requires a package path.
    goPackagePath = "github.com/google/nixery";

    goDeps = ./go-deps.nix;
    src    = ./.;

    meta = {
      description = "Container image build serving Nix-backed images";
      homepage    = "https://github.com/google/nixery";
      license     = lib.licenses.asl20;
      maintainers = [ lib.maintainers.tazjin ];
    };
  };

  # Nix expression (unimported!) which is used by Nixery to build
  # container images.
  nixery-builder = runCommand "build-registry-image.nix" {} ''
    cat ${./build-registry-image.nix} > $out
  '';

  # Static files to serve on the Nixery index. This is used primarily
  # for the demo instance running at nixery.appspot.com and provides
  # some background information for what Nixery is.
  nixery-static = runCommand "nixery-static" {} ''
    mkdir $out
    cp ${./static}/* $out
  '';

  # Wrapper script running the Nixery server with the above two data
  # dependencies configured.
  #
  # In most cases, this will be the derivation a user wants if they
  # are installing Nixery directly.
  nixery-bin = writeShellScriptBin "nixery" ''
    export NIX_BUILDER="${nixery-builder}"
    export WEB_DIR="${nixery-static}"
    exec ${nixery-server}/bin/nixery
  '';

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
      export NIX_SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt
      mkdir /tmp

      # Create the build user/group required by Nix
      echo 'nixbld:x:30000:nixbld' >> /etc/group
      echo 'nixbld:x:30000:30000:nixbld:/tmp:/bin/bash' >> /etc/passwd

      # Disable sandboxing to avoid running into privilege issues
      mkdir -p /etc/nix
      echo 'sandbox = false' >> /etc/nix/nix.conf

      exec ${nixery-bin}/bin/nixery
    '';
  in dockerTools.buildLayeredImage {
    name = "nixery";
    config.Cmd = ["${nixery-launch-script}/bin/nixery"];
    contents = [
      cacert
      coreutils
      git
      gnutar
      gzip
      nix
      nixery-launch-script
      openssh
    ];
  };
}
