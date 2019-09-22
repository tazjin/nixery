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
  # Package set to used (this will usually be loaded by load-pkgs.nix)
  pkgs,
  # Image Name
  name,
  # Image tag, the Nix output's hash will be used if null
  tag ? null,
  # Tool used to determine layer grouping
  groupLayers,
  # Files to put on the image (a nix store path or list of paths).
  contents ? [],
  # Packages to install by name (which must refer to top-level attributes of
  # nixpkgs). This is passed in as a JSON-array in string form.
  packages ? "[]",
  # Docker's modern image storage mechanisms have a maximum of 125
  # layers. To allow for some extensibility (via additional layers),
  # the default here is set to something a little less than that.
  maxLayers ? 96,
  # Popularity data for layer solving is fetched from the URL passed
  # in here.
  popularityUrl ? "https://storage.googleapis.com/nixery-layers/popularity/popularity-19.03.173490.5271f8dddc0.json",

  ...
}:

with builtins;

let
  inherit (pkgs) lib runCommand writeText;

  tarLayer = "application/vnd.docker.image.rootfs.diff.tar";
  baseName = baseNameOf name;

  # deepFetch traverses the top-level Nix package set to retrieve an item via a
  # path specified in string form.
  #
  # For top-level items, the name of the key yields the result directly. Nested
  # items are fetched by using dot-syntax, as in Nix itself.
  #
  # Due to a restriction of the registry API specification it is not possible to
  # pass uppercase characters in an image name, however the Nix package set
  # makes use of camelCasing repeatedly (for example for `haskellPackages`).
  #
  # To work around this, if no value is found on the top-level a second lookup
  # is done on the package set using lowercase-names. This is not done for
  # nested sets, as they often have keys that only differ in case.
  #
  # For example, `deepFetch pkgs "xorg.xev"` retrieves `pkgs.xorg.xev` and
  # `deepFetch haskellpackages.stylish-haskell` retrieves
  # `haskellPackages.stylish-haskell`.
  deepFetch = with lib; s: n:
    let path = splitString "." n;
        err = { error = "not_found"; pkg = n; };
        # The most efficient way I've found to do a lookup against
        # case-differing versions of an attribute is to first construct a
        # mapping of all lowercased attribute names to their differently cased
        # equivalents.
        #
        # This map is then used for a second lookup if the top-level
        # (case-sensitive) one does not yield a result.
        hasUpper = str: (match ".*[A-Z].*" str) != null;
        allUpperKeys = filter hasUpper (attrNames s);
        lowercased = listToAttrs (map (k: {
          name = toLower k;
          value = k;
          }) allUpperKeys);
        caseAmendedPath = map (v: if hasAttr v lowercased then lowercased."${v}" else v) path;
        fetchLower = attrByPath caseAmendedPath err s;
    in attrByPath path fetchLower s;

  # allContents is the combination of all derivations and store paths passed in
  # directly, as well as packages referred to by name.
  #
  # It accumulates potential errors about packages that could not be found to
  # return this information back to the server.
  allContents =
    # Folds over the results of 'deepFetch' on all requested packages to
    # separate them into errors and content. This allows the program to
    # terminate early and return only the errors if any are encountered.
    let splitter = attrs: res:
          if hasAttr "error" res
          then attrs // { errors = attrs.errors ++ [ res ]; }
          else attrs // { contents = attrs.contents ++ [ res ]; };
        init = { inherit contents; errors = []; };
        fetched = (map (deepFetch pkgs) (fromJSON packages));
    in foldl' splitter init fetched;

  popularity = builtins.fetchurl popularityUrl;

  # Before actually creating any image layers, the store paths that need to be
  # included in the image must be sorted into the layers that they should go
  # into.
  #
  # How contents are allocated to each layer is decided by the `group-layers.go`
  # program. The mechanism used is described at the top of the program's source
  # code, or alternatively in the layering design document:
  #
  #   https://storage.googleapis.com/nixdoc/nixery-layers.html
  #
  # To invoke the program, a graph of all runtime references is created via
  # Nix's exportReferencesGraph feature - the resulting layers are read back
  # into Nix using import-from-derivation.
  groupedLayers = fromJSON (readFile (runCommand "grouped-layers.json" {
    __structuredAttrs = true;
    exportReferencesGraph.graph = allContents.contents;
    PATH = "${groupLayers}/bin";
    builder = toFile "builder" ''
      . .attrs.sh
      group-layers --budget ${toString (maxLayers - 1)} --pop ${popularity} --out ''${outputs[out]}
    '';
  } ""));

  # Given a list of store paths, create an image layer tarball with
  # their contents.
  pathsToLayer = paths: runCommand "layer.tar" {
  } ''
    tar --no-recursion -Prf "$out" \
        --mtime="@$SOURCE_DATE_EPOCH" \
        --owner=0 --group=0 /nix /nix/store

    tar -Prpf "$out" --hard-dereference --sort=name \
        --mtime="@$SOURCE_DATE_EPOCH" \
        --owner=0 --group=0 ${lib.concatStringsSep " " paths}
  '';

  bulkLayers = writeText "bulk-layers"
  (lib.concatStringsSep "\n" (map (layer: pathsToLayer layer.contents)
                                   groupedLayers));

  # Create a symlink forest into all top-level store paths.
  contentsEnv = pkgs.symlinkJoin {
    name = "bulk-layers";
    paths = allContents.contents;
  };

  # This customisation layer which contains the symlink forest
  # required at container runtime is assembled with a simplified
  # version of dockerTools.mkCustomisationLayer.
  #
  # No metadata creation (such as layer hashing) is required when
  # serving images over the API.
  customisationLayer = runCommand "customisation-layer.tar" {} ''
    cp -r ${contentsEnv}/ ./layer
    tar --transform='s|^\./||' -C layer --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 -cf $out .
  '';

  # Inspect the returned bulk layers to determine which layers belong to the
  # image and how to serve them.
  #
  # This computes both an MD5 and a SHA256 hash of each layer, which are used
  # for different purposes. See the registry server implementation for details.
  allLayersJson = runCommand "fs-layer-list.json" {
    buildInputs = with pkgs; [ coreutils jq openssl ];
  } ''
      cat ${bulkLayers} | sort -t/ -k5 -n > layer-list
      echo ${customisationLayer} >> layer-list

      for layer in $(cat layer-list); do
        layerSha256=$(sha256sum $layer | cut -d ' ' -f1)
        # The server application compares binary MD5 hashes and expects base64
        # encoding instead of hex.
        layerMd5=$(openssl dgst -md5 -binary $layer | openssl enc -base64)
        layerSize=$(stat --printf '%s' $layer)

        jq -n -c --arg sha256 $layerSha256 --arg md5 $layerMd5 --arg size $layerSize --arg path $layer \
          '{ size: ($size | tonumber), sha256: $sha256, md5: $md5, path: $path }' >> fs-layers
      done

      cat fs-layers | jq -s -c '.' > $out
  '';
  allLayers = fromJSON (readFile allLayersJson);

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
  configJson = writeText "${baseName}-config.json" (toJSON config);
  configMetadata = fromJSON (readFile (runCommand "config-meta" {
    buildInputs = with pkgs; [ jq openssl ];
  } ''
    size=$(stat --printf '%s' ${configJson})
    sha256=$(sha256sum ${configJson} | cut -d ' ' -f1)
    md5=$(openssl dgst -md5 -binary ${configJson} | openssl enc -base64)
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
    } // (listToAttrs (map (layer: {
      name  = "${layer.sha256}";
      value = {
        path = layer.path;
        md5 = layer.md5;
      };
    }) allLayers));

  # Final output structure returned to the controller in the case of a
  # successful build.
  manifestOutput = {
    inherit manifest layerLocations;
  };

  # Output structure returned if errors occured during the build. Currently the
  # only error type that is returned in a structured way is 'not_found'.
  errorOutput = {
    error = "not_found";
    pkgs = map (err: err.pkg) allContents.errors;
  };
in writeText "manifest-output.json" (if (length allContents.errors) == 0
  then toJSON manifestOutput
  else toJSON errorOutput
)
