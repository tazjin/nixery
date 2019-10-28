<div align="center">
  <img src="docs/src/nixery-logo.png">
</div>

-----------------

[![Build Status](https://travis-ci.org/google/nixery.svg?branch=master)](https://travis-ci.org/google/nixery)

**Nixery** is a Docker-compatible container registry that is capable of
transparently building and serving container images using [Nix][].

Images are built on-demand based on the *image name*. Every package that the
user intends to include in the image is specified as a path component of the
image name.

The path components refer to top-level keys in `nixpkgs` and are used to build a
container image using a [layering strategy][] that optimises for caching popular
and/or large dependencies.

A public instance as well as additional documentation is available at
[nixery.dev][public].

The project started out inspired by the [buildLayeredImage][] blog post with the
intention of becoming a Kubernetes controller that can serve declarative image
specifications specified in CRDs as container images. The design for this was
outlined in [a public gist][gist].

This is not an officially supported Google project.

## Demo

Click the image to see an example in which an image containing an interactive
shell and GNU `hello` is downloaded.

[![asciicast](https://asciinema.org/a/262583.png)](https://asciinema.org/a/262583?autoplay=1)

To try it yourself, head to [nixery.dev][public]!

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

* `PORT`: HTTP port on which Nixery should listen
* `NIXERY_CHANNEL`: The name of a Nix/NixOS channel to use for building
* `NIXERY_PKGS_REPO`: URL of a git repository containing a package set (uses
  locally configured SSH/git credentials)
* `NIXERY_PKGS_PATH`: A local filesystem path containing a Nix package set to
  use for building
* `NIXERY_STORAGE_BACKEND`: The type of backend storage to use, currently
  supported values are `gcs` (Google Cloud Storage) and `filesystem`.

  For each of these additional backend configuration is necessary, see the
  [storage section](#storage) for details.
* `NIX_TIMEOUT`: Number of seconds that any Nix builder is allowed to run
  (defaults to 60)
* `NIX_POPULARITY_URL`: URL to a file containing popularity data for
  the package set (see `popcount/`)

If the `GOOGLE_APPLICATION_CREDENTIALS` environment variable is set to a service
account key, Nixery will also use this key to create [signed URLs][] for layers
in the storage bucket. This makes it possible to serve layers from a bucket
without having to make them publicly available.

### Storage

Nixery supports multiple different storage backends in which its build cache and
image layers are kept, and from which they are served.

Currently the available storage backends are Google Cloud Storage and the local
file system.

In the GCS case, images are served by redirecting clients to the storage bucket.
Layers stored on the filesystem are served straight from the local disk.

These extra configuration variables must be set to configure storage backends:

* `GCS_BUCKET`: Name of the Google Cloud Storage bucket to use (**required** for
  `gcs`)
* `GOOGLE_APPLICATION_CREDENTIALS`: Path to a GCP service account JSON key
  (**optional** for `gcs`)
* `STORAGE_PATH`: Path to a folder in which to store and from which to serve
  data (**required** for `filesystem`)

## Roadmap

### Kubernetes integration

It should be trivial to deploy Nixery inside of a Kubernetes cluster with
correct caching behaviour, addressing and so on.

See [issue #4](https://github.com/google/nixery/issues/4).

[Nix]: https://nixos.org/
[layering strategy]: https://storage.googleapis.com/nixdoc/nixery-layers.html
[gist]: https://gist.github.com/tazjin/08f3d37073b3590aacac424303e6f745
[buildLayeredImage]: https://grahamc.com/blog/nix-and-layered-docker-images
[public]: https://nixery.dev
[gcs]: https://cloud.google.com/storage/
