<div align="center">
  <img src="static/nixery-logo.png">
</div>

-----------------

[![Build Status](https://travis-ci.org/google/nixery.svg?branch=master)](https://travis-ci.org/google/nixery)

**Nixery** is a Docker-compatible container registry that is capable of
transparently building and serving container images using [Nix][].

Images are built on-demand based on the *image name*. Every package that the
user intends to include in the image is specified as a path component of the
image name.

The path components refer to top-level keys in `nixpkgs` and are used to build a
container image using Nix's [buildLayeredImage][] functionality.

The project started out with the intention of becoming a Kubernetes controller
that can serve declarative image specifications specified in CRDs as container
images. The design for this is outlined in [a public gist][gist].

An example instance is available at [nixery.dev][demo].

This is not an officially supported Google project.

## Usage example

Using the publicly available Nixery instance at `nixery.dev`, one could
retrieve a container image containing `curl` and an interactive shell like this:

```shell
tazjin@tazbox:~$ sudo docker run -ti nixery.dev/shell/curl bash
Unable to find image 'nixery.dev/shell/curl:latest' locally
latest: Pulling from shell/curl
7734b79e1ba1: Already exists
b0d2008d18cd: Pull complete
< ... some layers omitted ...>
Digest: sha256:178270bfe84f74548b6a43347d73524e5c2636875b673675db1547ec427cf302
Status: Downloaded newer image for nixery.dev/shell/curl:latest
bash-4.4# curl --version
curl 7.64.0 (x86_64-pc-linux-gnu) libcurl/7.64.0 OpenSSL/1.0.2q zlib/1.2.11 libssh2/1.8.0 nghttp2/1.35.1
```

The special meta-package `shell` provides an image base with many core
components (such as `bash` and `coreutils`) that users commonly expect in
interactive images.

## Feature overview

* Serve container images on-demand using image names as content specifications

  Specify package names as path components and Nixery will create images, using
  the most efficient caching strategy it can to share data between different
  images.

* Use private package sets from various sources

  In addition to building images from the publicly available Nix/NixOS channels,
  a private Nixery instance can be configured to serve images built from a
  package set hosted in a custom git repository or filesystem path.

  When using this feature with custom git repositories, Nixery will forward the
  specified image tags as git references.

  For example, if a company used a custom repository overlaying their packages
  on the Nix package set, images could be built from a git tag `release-v2`:

  `docker pull nixery.thecompany.website/custom-service:release-v2`

* Efficient serving of image layers from Google Cloud Storage

  After building an image, Nixery stores all of its layers in a GCS bucket and
  forwards requests to retrieve layers to the bucket. This enables efficient
  serving of layers, as well as sharing of image layers between redundant
  instances.

## Configuration

Nixery supports the following configuration options, provided via environment
variables:

* `BUCKET`: [Google Cloud Storage][gcs] bucket to store & serve image layers
* `PORT`: HTTP port on which Nixery should listen
* `NIXERY_CHANNEL`: The name of a Nix/NixOS channel to use for building
* `NIXERY_PKGS_REPO`: URL of a git repository containing a package set (uses
  locally configured SSH/git credentials)
* `NIXERY_PKGS_PATH`: A local filesystem path containing a Nix package set to use
  for building
* `GCS_SIGNING_KEY`: A Google service account key (in PEM format) that can be
  used to sign Cloud Storage URLs
* `GCS_SIGNING_ACCOUNT`: Google service account ID that the signing key belongs
  to

## Roadmap

### Kubernetes integration (in the future)

It should be trivial to deploy Nixery inside of a Kubernetes cluster with
correct caching behaviour, addressing and so on.

See [issue #4](https://github.com/google/nixery/issues/4).

[Nix]: https://nixos.org/
[gist]: https://gist.github.com/tazjin/08f3d37073b3590aacac424303e6f745
[buildLayeredImage]: https://grahamc.com/blog/nix-and-layered-docker-images
[demo]: https://nixery.dev
[gcs]: https://cloud.google.com/storage/
