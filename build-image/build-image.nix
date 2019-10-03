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

# This file contains a derivation that outputs structured information
# about the runtime dependencies of an image with a given set of
# packages. This is used by Nixery to determine the layer grouping and
# assemble each layer.
#
# In addition it creates and outputs a meta-layer with the symlink
# structure required for using the image together with the individual
# package layers.

{
  # Description of the package set to be used (will be loaded by load-pkgs.nix)
  srcType ? "nixpkgs",
  srcArgs ? "nixos-19.03",
  importArgs ? { },
  # Path to load-pkgs.nix
  loadPkgs ? ./load-pkgs.nix,
  # Packages to install by name (which must refer to top-level attributes of
  # nixpkgs). This is passed in as a JSON-array in string form.
  packages ? "[]"
}:

let
  inherit (builtins)
    foldl'
    fromJSON
    hasAttr
    length
    readFile
    toFile
    toJSON;

  inherit (pkgs) lib runCommand writeText;

  pkgs = import loadPkgs { inherit srcType srcArgs importArgs; };

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

  # allContents contains all packages successfully retrieved by name
  # from the package set, as well as any errors encountered while
  # attempting to fetch a package.
  #
  # Accumulated error information is returned back to the server.
  allContents =
    # Folds over the results of 'deepFetch' on all requested packages to
    # separate them into errors and content. This allows the program to
    # terminate early and return only the errors if any are encountered.
    let splitter = attrs: res:
          if hasAttr "error" res
          then attrs // { errors = attrs.errors ++ [ res ]; }
          else attrs // { contents = attrs.contents ++ [ res ]; };
        init = { contents = []; errors = []; };
        fetched = (map (deepFetch pkgs) (fromJSON packages));
    in foldl' splitter init fetched;

  # Contains the export references graph of all retrieved packages,
  # which has information about all runtime dependencies of the image.
  #
  # This is used by Nixery to group closures into image layers.
  runtimeGraph = runCommand "runtime-graph.json" {
    __structuredAttrs = true;
    exportReferencesGraph.graph = allContents.contents;
    PATH = "${pkgs.coreutils}/bin";
    builder = toFile "builder" ''
      . .attrs.sh
      cp .attrs.json ''${outputs[out]}
    '';
  } "";

  # Create a symlink forest into all top-level store paths of the
  # image contents.
  contentsEnv = pkgs.symlinkJoin {
    name = "bulk-layers";
    paths = allContents.contents;
  };

  # Image layer that contains the symlink forest created above. This
  # must be included in the image to ensure that the filesystem has a
  # useful layout at runtime.
  symlinkLayer = runCommand "symlink-layer.tar" {} ''
    cp -r ${contentsEnv}/ ./layer
    tar --transform='s|^\./||' -C layer --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 -cf $out .
  '';

  # Metadata about the symlink layer which is required for serving it.
  # Two different hashes are computed for different usages (inclusion
  # in manifest vs. content-checking in the layer cache).
  symlinkLayerMeta = fromJSON (readFile (runCommand "symlink-layer-meta.json" {
    buildInputs = with pkgs; [ coreutils jq openssl ];
  }''
    layerSha256=$(sha256sum ${symlinkLayer} | cut -d ' ' -f1)
    layerSize=$(stat --printf '%s' ${symlinkLayer})

    jq -n -c --arg sha256 $layerSha256 --arg size $layerSize --arg path ${symlinkLayer} \
      '{ size: ($size | tonumber), sha256: $sha256, path: $path }' >> $out
  ''));

  # Final output structure returned to Nixery if the build succeeded
  buildOutput = {
    runtimeGraph = fromJSON (readFile runtimeGraph);
    symlinkLayer = symlinkLayerMeta;
  };

  # Output structure returned if errors occured during the build. Currently the
  # only error type that is returned in a structured way is 'not_found'.
  errorOutput = {
    error = "not_found";
    pkgs = map (err: err.pkg) allContents.errors;
  };
in writeText "build-output.json" (if (length allContents.errors) == 0
  then toJSON buildOutput
  else toJSON errorOutput
)
