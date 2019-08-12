{ buildGoPackage, lib }:

buildGoPackage {
  name   = "nixery-server";
  goDeps = ./go-deps.nix;
  src    = ./.;

  goPackagePath = "github.com/google/nixery";

  meta = {
    description = "Container image builder serving Nix-backed images";
    homepage = "https://github.com/google/nixery";
    license = lib.licenses.asl20;
    maintainers = [ lib.maintainers.tazjin ];
  };
}
