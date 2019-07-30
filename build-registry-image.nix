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

# This file contains a modified version of dockerTools.buildImage that, instead
# of outputting a single tarball which can be imported into a running Docker
# daemon, builds a manifest file that can be used for serving the image over a
# registry API.

{
  # Image Name
  name,
  # Image tag, the Nix's output hash will be used if null
  tag ? null,
  # Files to put on the image (a nix store path or list of paths).
  contents ? [],
  # Packages to install by name (which must refer to top-level attributes of
  # nixpkgs). This is passed in as a JSON-array in string form.
  packages ? "[]",
  # Optional bash script to run on the files prior to fixturizing the layer.
  extraCommands ? "", uid ? 0, gid ? 0,
  # Docker's lowest maximum layer limit is 42-layers for an old
  # version of the AUFS graph driver. We pick 24 to ensure there is
  # plenty of room for extension. I believe the actual maximum is
  # 128.
  maxLayers ? 24,
  # Nix channel to use
  channel ? "nixos-19.03"
}:

# Import the specified channel directly from Github.
let
  channelUrl = "https://github.com/NixOS/nixpkgs-channels/archive/${channel}.tar.gz";
  pkgs = import (builtins.fetchTarball channelUrl) {};
in

# Since this is essentially a re-wrapping of some of the functionality that is
# implemented in the dockerTools, we need all of its components in our top-level
# namespace.
with pkgs;
with dockerTools;

let
  tarLayer = "application/vnd.docker.image.rootfs.diff.tar";
  baseName = baseNameOf name;

  # deepFetch traverses the top-level Nix package set to retrieve an item via a
  # path specified in string form.
  #
  # For top-level items, the name of the key yields the result directly. Nested
  # items are fetched by using dot-syntax, as in Nix itself.
  #
  # For example, `deepFetch pkgs "xorg.xev"` retrieves `pkgs.xorg.xev`.
  deepFetch = s: n:
    let path = lib.strings.splitString "." n;
        err = builtins.throw "Could not find '${n}' in package set";
    in lib.attrsets.attrByPath path err s;

  # allContents is the combination of all derivations and store paths passed in
  # directly, as well as packages referred to by name.
  allContents = contents ++ (map (deepFetch pkgs) (builtins.fromJSON packages));

  contentsEnv = symlinkJoin {
    name = "bulk-layers";
    paths = allContents;
  };

  # The image build infrastructure expects to be outputting a slightly different
  # format than the one we serve over the registry protocol. To work around its
  # expectations we need to provide an empty JSON file that it can write some
  # fun data into.
  emptyJson = writeText "empty.json" "{}";

  bulkLayers = mkManyPureLayers {
    name = baseName;
    configJson = emptyJson;
    closure = writeText "closure" "${contentsEnv} ${emptyJson}";
    # One layer will be taken up by the customisationLayer, so
    # take up one less.
    maxLayers = maxLayers - 1;
  };

  customisationLayer = mkCustomisationLayer {
    name = baseName;
    contents = contentsEnv;
    baseJson = emptyJson;
    inherit uid gid extraCommands;
  };

  # Inspect the returned bulk layers to determine which layers belong to the
  # image and how to serve them.
  #
  # This computes both an MD5 and a SHA256 hash of each layer, which are used
  # for different purposes. See the registry server implementation for details.
  #
  # Some of this logic is copied straight from `buildLayeredImage`.
  allLayersJson = runCommand "fs-layer-list.json" {
    buildInputs = [ coreutils findutils jq openssl ];
  } ''
      find ${bulkLayers} -mindepth 1 -maxdepth 1 | sort -t/ -k5 -n > layer-list
      echo ${customisationLayer} >> layer-list

      for layer in $(cat layer-list); do
        layerPath="$layer/layer.tar"
        layerSha256=$(sha256sum $layerPath | cut -d ' ' -f1)
        # The server application compares binary MD5 hashes and expects base64
        # encoding instead of hex.
        layerMd5=$(openssl dgst -md5 -binary $layerPath | openssl enc -base64)
        layerSize=$(wc -c $layerPath | cut -d ' ' -f1)

        jq -n -c --arg sha256 $layerSha256 --arg md5 $layerMd5 --arg size $layerSize --arg path $layerPath \
          '{ size: ($size | tonumber), sha256: $sha256, md5: $md5, path: $path }' >> fs-layers
      done

      cat fs-layers | jq -s -c '.' > $out
  '';
  allLayers = builtins.fromJSON (builtins.readFile allLayersJson);

  # Image configuration corresponding to the OCI specification for the file type
  # 'application/vnd.oci.image.config.v1+json'
  config = {
    architecture = "amd64";
    os = "linux";
    rootfs.type = "layers";
    rootfs.diff_ids = map (layer: "sha256:${layer.sha256}") allLayers;
    # Required to let Kubernetes import Nixery images
    config = {};
  };
  configJson = writeText "${baseName}-config.json" (builtins.toJSON config);
  configMetadata = with builtins; fromJSON (readFile (runCommand "config-meta" {
    buildInputs = [ jq openssl ];
  } ''
    size=$(wc -c ${configJson} | cut -d ' ' -f1)
    sha256=$(sha256sum ${configJson} | cut -d ' ' -f1)
    md5=$(openssl dgst -md5 -binary $layerPath | openssl enc -base64)
    jq -n -c --arg size $size --arg sha256 $sha256 --arg md5 $md5 \
      '{ size: ($size | tonumber), sha256: $sha256, md5: $md5 }' \
      >> $out
  ''));

  # Corresponds to the manifest JSON expected by the Registry API.
  #
  # This is Docker's "Image Manifest V2, Schema 2":
  #   https://docs.docker.com/registry/spec/manifest-v2-2/
  manifest = {
    schemaVersion = 2;
    mediaType = "application/vnd.docker.distribution.manifest.v2+json";

    config = {
      mediaType = "application/vnd.docker.container.image.v1+json";
      size = configMetadata.size;
      digest = "sha256:${configMetadata.sha256}";
    };

    layers = map (layer: {
      mediaType = tarLayer;
      digest = "sha256:${layer.sha256}";
      size = layer.size;
    }) allLayers;
  };

  # This structure maps each layer digest to the actual tarball that will need
  # to be served. It is used by the controller to cache the paths during a pull.
  layerLocations = {
      "${configMetadata.sha256}" = {
        path = configJson;
        md5 = configMetadata.md5;
      };
    } // (builtins.listToAttrs (map (layer: {
      name  = "${layer.sha256}";
      value = {
        path = layer.path;
        md5 = layer.md5;
      };
    }) allLayers));

in writeText "manifest-output.json" (builtins.toJSON {
  inherit manifest layerLocations;
})
