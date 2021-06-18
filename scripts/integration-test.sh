#!/usr/bin/env bash
set -eou pipefail

# This integration test makes sure that the container image built
# for Nixery itself runs fine in Docker, and that images pulled
# from it work in Docker.

IMG=$(docker load -q -i "$(nix-build -A nixery-image)" | awk '{ print $3 }')
echo "Loaded Nixery image as ${IMG}"

# Run the built nixery docker image in the background, but keep printing its
# output as it occurs.
# We can't just mount a tmpfs to /var/cache/nixery, as tmpfs doesn't support
# user xattrs.
# So create a temporary directory in the current working directory, and hope
# it's backed by something supporting user xattrs.
# We'll notice it isn't if nixery starts complaining about not able to set
# xattrs anyway.
if [ -d var-cache-nixery ]; then rm -Rf var-cache-nixery; fi
mkdir var-cache-nixery
docker run --privileged --rm -p 8080:8080 --name nixery \
  -e PORT=8080 \
  --mount "type=bind,source=${PWD}/var-cache-nixery,target=/var/cache/nixery" \
  -e NIXERY_CHANNEL=nixos-unstable \
  -e NIXERY_STORAGE_BACKEND=filesystem \
  -e STORAGE_PATH=/var/cache/nixery \
  "${IMG}" &

# Give the container ~20 seconds to come up
set +e
attempts=0
echo -n "Waiting for Nixery to start ..."
until curl --fail --silent "http://localhost:8080/v2/"; do
  [[ attempts -eq 30 ]] && echo "Nixery container failed to start!" && exit 1
  ((attempts++))
  echo -n "."
  sleep 1
done
set -e

# Pull and run an image of the current CPU architecture
case $(uname -m) in
  x86_64)
    docker run --rm localhost:8080/hello hello
    ;;
  aarch64)
    docker run --rm localhost:8080/arm64/hello hello
    ;;
esac

# Pull an image of the opposite CPU architecture (but without running it)
case $(uname -m) in
x86_64)
  docker pull localhost:8080/arm64/hello
  ;;
aarch64)
  docker pull localhost:8080/hello
  ;;
esac
