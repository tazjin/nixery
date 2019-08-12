popcount
========

This script is used to count the popularity for each package in `nixpkgs`, by
determining how many other packages depend on it.

It skips over all packages that fail to build, are not cached or are unfree -
but these omissions do not meaningfully affect the statistics.

It currently does not evaluate nested attribute sets (such as
`haskellPackages`).

## Usage

1. Generate a list of all top-level attributes in `nixpkgs`:

   ```shell
   nix eval '(with builtins; toJSON (attrNames (import <nixpkgs> {})))' | jq -r | jq > all-top-level.json
   ```

2. Run `./popcount > all-runtime-deps.txt`

3. Collect and count the results with the following magic incantation:

   ```shell
   cat all-runtime-deps.txt \
     | sed -r 's|/nix/store/[a-z0-9]+-||g' \
     | sort \
     | uniq -c \
     | sort -n -r \
     | awk '{ print "{\"" $2 "\":" $1 "}"}' \
     | jq -c -s '. | add | with_entries(select(.value > 1))' \
     > your-output-file
   ```

   In essence, this will trim Nix's store paths and hashes from the output,
   count the occurences of each package and return the output as JSON. All
   packages that have no references other than themselves are removed from the
   output.
