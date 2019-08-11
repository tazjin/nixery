{ pkgs ? import <nixpkgs> { config.allowUnfree = false; }
, target }:

let
  inherit (pkgs) coreutils runCommand writeText;
  inherit (builtins) replaceStrings readFile toFile fromJSON toJSON foldl' listToAttrs;

  path = [ pkgs."${target}" ];

  # graphJSON abuses feature in Nix that makes structured runtime
  # closure information available to builders. This data is imported
  # back via IFD to process it for layering data.
  graphJSON =
    path:
    runCommand "build-graph" {
      __structuredAttrs = true;
      exportReferencesGraph.graph = path;
      PATH = "${coreutils}/bin";
      builder = toFile "builder" ''
        . .attrs.sh
        cat .attrs.json > ''${outputs[out]}
      '';
    } "";

  buildClosures = paths: (fromJSON (readFile (graphJSON paths)));

  buildGraph = paths: listToAttrs (map (c: {
    name = c.path;
    value = {
    inherit (c) closureSize references;
    };
  }) (buildClosures paths));

  # Nix does not allow attrbute set keys to refer to store paths, but
  # we need them to for the purpose of the calculation. To work around
  # it, the store path prefix is replaced with the string 'closure/'
  # and later replaced again.
  fromStorePath = replaceStrings [ "/nix/store" ] [ "closure/" ];
  toStorePath = replaceStrings [ "closure/" ] [ "/nix/store/" ];

  buildTree = paths:
  let
    graph = buildGraph paths;
    top = listToAttrs (map (p: {
      name = fromStorePath (toString p);
      value = {};
    }) paths);
  in top;

  outputJson = thing: writeText "the-thing.json" (builtins.toJSON thing);
in outputJson (buildClosures path).graph
