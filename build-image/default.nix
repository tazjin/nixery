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

# This file builds the tool used to calculate layer distribution and
# moves the files needed to call the Nix builds at runtime in the
# correct locations.

{ pkgs ? null, self ? ./.

  # Because of the insanity occuring below, this function must mirror
  # all arguments of build-image.nix.
, pkgSource ? "nixpkgs!nixos-19.03"
, tag ? null, name ? null, packages ? null, maxLayers ? null
}@args:

let pkgs = import ./load-pkgs.nix { inherit pkgSource; };
in with pkgs; rec {

  groupLayers = buildGoPackage {
    name = "group-layers";
    goDeps = ./go-deps.nix;
    goPackagePath = "github.com/google/nixery/group-layers";

    # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
    #                    WARNING: HERE BE DRAGONS!                    #
    #                                                                 #
    # The hack below is used to break evaluation purity. The issue is #
    # that Nixery's build instructions (the default.nix in the folder #
    # above this one) must build a program that can invoke Nix at     #
    # runtime, with a derivation that needs a program tracked in this #
    # source tree (`group-layers`).                                   #
    #                                                                 #
    # Simply installing that program in the $PATH of Nixery does not  #
    # work, because the runtime Nix builds use their own isolated     #
    # environment.                                                    #
    #                                                                 #
    # I first attempted to naively copy the sources into the Nix      #
    # store, so that Nixery could build `group-layers` when it starts #
    # up - however those sources are not available to a nested Nix    #
    # build because they're not part of the context of the nested     #
    # invocation.                                                     #
    #                                                                 #
    # Nix has several primitives under `builtins.` that can break     #
    # evaluation purity, these (namely readDir and readFile) are used #
    # below to break out of the isolated environment and reconstruct  #
    # the source tree for `group-layers`.                             #
    #                                                                 #
    # There might be a better way to do this, but I don't know what   #
    # it is.                                                          #
    # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # #
    src = runCommand "group-layers-srcs" { } ''
      mkdir -p $out
      ${with builtins;
      let
        files =
          (attrNames (lib.filterAttrs (_: t: t != "symlink") (readDir self)));
        commands =
          map (f: "cp ${toFile f (readFile "${self}/${f}")} $out/${f}") files;
      in lib.concatStringsSep "\n" commands}
    '';

    meta = {
      description =
        "Tool to group a set of packages into container image layers";
      license = lib.licenses.asl20;
      maintainers = [ lib.maintainers.tazjin ];
    };
  };

  buildImage = import ./build-image.nix
    ({ inherit pkgs groupLayers; } // (lib.filterAttrs (_: v: v != null) args));

  # Wrapper script which is called by the Nixery server to trigger an
  # actual image build. This exists to avoid having to specify the
  # location of build-image.nix at runtime.
  wrapper = writeShellScriptBin "nixery-build-image" ''
    exec ${nix}/bin/nix-build \
      --show-trace \
      --no-out-link "$@" \
      --argstr self "${./.}" \
      -A buildImage ${./.}
  '';
}
