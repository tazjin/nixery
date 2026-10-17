popcount
========

This tool counts the popularity of each package in a Nix channel, by
determining how many other packages (i.e. store paths in the channel's
binary cache) depend on it.

It skips over all packages that fail to build, are not cached or are
unfree.

It currently does not evaluate nested attribute sets (such as
`haskellPackages`).

## Usage

Run it against a Nix channel (for example the one pinned in
`//third_party/sources`):

```shell
nixery-popcount -cache=/var/cache/nixery-popcount \
  https://releases.nixos.org/nixos/unstable/nixos-26.11pre1080855.7a0f122f5090/nixexprs.tar.xz \
  > popularity.json
```

The `-cache` directory persists downloaded narinfo files between runs,
which makes reruns significantly faster. The URL may point at the
channel directory or at any file inside it (such as `nixexprs.tar.xz`).

Output is written to stdout as a JSON map of package names to their
reference counts.
