# Nix

These sections are designed to give some background information on what Nix is.
If you've never heard of Nix before looking at Nixery, this might just be the
page for you!

[Nix][] is a functional package-manager that comes with a number of advantages
over traditional package managers, such as side-by-side installs of different
package versions, atomic updates, easy customisability, simple binary caching
and much more. Feel free to explore the [Nix website][Nix] for an overview of
Nix itself.

Nix uses a custom programming language also called Nix, which is explained here
[on its own page][nix-1p].

In addition to the package manager and language, the Nix project also maintains
[NixOS][] - a Linux distribution built entirely on Nix. On NixOS, users can
declaratively describe the *entire* configuration of their system and perform
updates/rollbacks to other system configurations with ease.

Most Nix packages are tracked in the [Nix package set][nixpkgs], usually simply
referred to as `nixpkgs`. It contains tens of thousands of packages already!

Nixery (which you are looking at!) provides an easy & simple way to get started
with Nix, in fact you don't even need to know that you're using Nix to make use
of Nixery.

[Nix]: https://nixos.org/nix/
[nix-1p]: nix-1p.html
[NixOS]: https://nixos.org/
[nixpkgs]: https://github.com/nixos/nixpkgs
