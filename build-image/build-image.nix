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
  # Docker's modern image storage mechanisms have a maximum of 125
  # layers. To allow for some extensibility (via additional layers),
  # the default here is set to something a little less than that.
  maxLayers ? 96,

  # Configuration for which package set to use when building.
  #
  # Both channels of the public nixpkgs repository as well as imports
  # from private repositories are supported.
  #
  # This setting can be invoked with three different formats:
  #
  # 1. nixpkgs!$channel (e.g. nixpkgs!nixos-19.03)
  # 2. git!$repo!$rev (e.g. git!git@github.com:NixOS/nixpkgs.git!master)
  # 3. path!$path (e.g. path!/var/local/nixpkgs)
  #
  # '!' was chosen as the separator because `builtins.split` does not
  # support regex escapes and there are few other candidates. It
  # doesn't matter much because this is invoked by the server.
  pkgSource ? "nixpkgs!nixos-19.03"
}:

let
  # If a nixpkgs channel is requested, it is retrieved from Github (as
  # a tarball) and imported.
  fetchImportChannel = channel:
  let url = "https://github.com/NixOS/nixpkgs-channels/archive/${channel}.tar.gz";
  in import (builtins.fetchTarball url) {};

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
    spec = if (builtins.stringLength rev) == 40 then {
      inherit url rev;
    } else {
      inherit url;
      ref = rev;
    };
  in import (builtins.fetchGit spec) {};

  importPath = path: import (builtins.toPath path) {};

  source = builtins.split "!" pkgSource;
  sourceType = builtins.elemAt source 0;
  pkgs = with builtins;
    if sourceType == "nixpkgs"
    then fetchImportChannel (elemAt source 2)
    else if sourceType == "git"
    then fetchImportGit (elemAt source 2) (elemAt source 4)
    else if sourceType == "path"
    then importPath (elemAt source 2)
    else builtins.throw("Invalid package set source specification: ${pkgSource}");
in

# Since this is essentially a re-wrapping of some of the functionality that is
# implemented in the dockerTools, we need all of its components in our top-level
# namespace.
with builtins;
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

  contentsEnv = symlinkJoin {
    name = "bulk-layers";
    paths = allContents.contents;
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
    buildInputs = [ jq openssl ];
  } ''
    size=$(wc -c ${configJson} | cut -d ' ' -f1)
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
