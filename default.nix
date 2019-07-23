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
      license     = lib.licenses.ascl20;
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
    cp -r ${./static} $out
  '';
}
