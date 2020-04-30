## Run your own Nixery

<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->

- [0. Prerequisites](#0-prerequisites)
- [1. Choose a package set](#1-choose-a-package-set)
- [2. Build Nixery itself](#2-build-nixery-itself)
- [3. Prepare configuration](#3-prepare-configuration)
- [4. Deploy Nixery](#4-deploy-nixery)
- [5. Productionise](#5-productionise)

<!-- markdown-toc end -->


---------

⚠ This page is still under construction! ⚠

--------

Running your own Nixery is not difficult, but requires some setup. Follow the
steps below to get up & running.

*Note:* Nixery can be run inside of a [GKE][] cluster, providing a local service
from which images can be requested. Documentation for how to set this up is
forthcoming, please see [nixery#4][].

## 0. Prerequisites

To run Nixery, you must have:

* [Nix][] (to build Nixery itself)
* Somewhere to run it (your own server, Google AppEngine, a Kubernetes cluster,
  whatever!)
* A [Google Cloud Storage][gcs] bucket in which to store & serve layers

## 1. Choose a package set

When running your own Nixery you need to decide which package set you want to
serve. By default, Nixery builds packages from a recent NixOS channel which
ensures that most packages are cached upstream and no expensive builds need to
be performed for trivial things.

However if you are running a private Nixery, chances are high that you intend to
use it with your own packages. There are three options available:

1. Specify an upstream Nix/NixOS channel[^1], such as `nixos-20.03` or
   `nixos-unstable`.
2. Specify your own git-repository with a custom package set[^2]. This makes it
   possible to pull different tags, branches or commits by modifying the image
   tag.
3. Specify a local file path containing a Nix package set. Where this comes from
   or what it contains is up to you.

## 2. Build Nixery itself

Building Nixery creates a container image. This section assumes that the
container runtime used is Docker, please modify instructions correspondingly if
you are using something else.

With a working Nix installation, building Nixery is done by invoking `nix-build
-A nixery-image` from a checkout of the [Nixery repository][repo].

This will create a `result`-symlink which points to a tarball containing the
image. In Docker, this tarball can be loaded by using `docker load -i result`.

## 3. Prepare configuration

Nixery is configured via environment variables.

You must set *all* of these:

* `BUCKET`: [Google Cloud Storage][gcs] bucket to store & serve image layers
* `PORT`: HTTP port on which Nixery should listen

You may set *one* of these, if unset Nixery defaults to `nixos-20.03`:

* `NIXERY_CHANNEL`: The name of a Nix/NixOS channel to use for building
* `NIXERY_PKGS_REPO`: URL of a git repository containing a package set (uses
  locally configured SSH/git credentials)
* `NIXERY_PKGS_PATH`: A local filesystem path containing a Nix package set to use
  for building

You may set *all* of these:

* `NIX_TIMEOUT`: Number of seconds that any Nix builder is allowed to run
  (defaults to 60)

To authenticate to the configured GCS bucket, Nixery uses Google's [Application
Default Credentials][ADC]. Depending on your environment this may require
additional configuration.

If the `GOOGLE_APPLICATION_CREDENTIALS` environment is configured, the service
account's private key will be used to create [signed URLs for
layers][signed-urls].

## 4. Deploy Nixery

With the above environment variables configured, you can run the image that was
built in step 2.

How this works depends on the environment you are using and is, for now, outside
of the scope of this tutorial.

Once Nixery is running you can immediately start requesting images from it.

## 5. Productionise

(⚠ Here be dragons! ⚠)

Nixery is still an early project and has not yet been deployed in any production
environments and some caveats apply.

Notably, Nixery currently does not support any authentication methods, so anyone
with network access to the registry can retrieve images.

Running a Nixery inside of a fenced-off environment (such as internal to a
Kubernetes cluster) should be fine, but you should consider to do all of the
following:

* Issue a TLS certificate for the hostname you are assigning to Nixery. In fact,
  Docker will refuse to pull images from registries that do not use TLS (with
  the exception of `.local` domains).
* Configure signed GCS URLs to avoid having to make your bucket world-readable.
* Configure request timeouts for Nixery if you have your own web server in front
  of it. This will be natively supported by Nixery in the future.

-------

[^1]: Nixery will not work with Nix channels older than `nixos-19.03`.

[^2]: This documentation will be updated with instructions on how to best set up
    a custom Nix repository. Nixery expects custom package sets to be a superset
    of `nixpkgs`, as it uses `lib` and other features from `nixpkgs`
    extensively.

[GKE]: https://cloud.google.com/kubernetes-engine/
[nixery#4]: https://github.com/google/nixery/issues/4
[Nix]: https://nixos.org/nix
[gcs]: https://cloud.google.com/storage/
[repo]: https://github.com/google/nixery
[signed-urls]: under-the-hood.html#5-image-layers-are-requested
[ADC]: https://cloud.google.com/docs/authentication/production#finding_credentials_automatically
