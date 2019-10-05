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

{ buildGoPackage, go, lib, srcHash }:

buildGoPackage rec {
  name = "nixery-server";
  goDeps = ./go-deps.nix;
  src = ./.;

  goPackagePath = "github.com/google/nixery/server";
  doCheck = true;

  # The following phase configurations work around the overengineered
  # Nix build configuration for Go.
  #
  # All I want this to do is produce a binary in the standard Nix
  # output path, so pretty much all the phases except for the initial
  # configuration of the "dependency forest" in $GOPATH have been
  # overridden.
  #
  # This is necessary because the upstream builder does wonky things
  # with the build arguments to the compiler, but I need to set some
  # complex flags myself

  outputs = [ "out" ];
  preConfigure = "bin=$out";
  buildPhase = ''
    runHook preBuild
    runHook renameImport

    export GOBIN="$out/bin"
    go install -ldflags "-X main.version=$(cat ${srcHash})" ${goPackagePath}
  '';

  fixupPhase = ''
    remove-references-to -t ${go} $out/bin/server
  '';

  checkPhase = ''
    go vet ${goPackagePath}
    go test ${goPackagePath}
  '';

  meta = {
    description = "Container image builder serving Nix-backed images";
    homepage = "https://github.com/google/nixery";
    license = lib.licenses.asl20;
    maintainers = [ lib.maintainers.tazjin ];
  };
}
