# Nixery

This package implements a Docker-compatible container registry that is capable
of transparently building and serving container images using [Nix][].

The project started out with the intention of becoming a Kubernetes controller
that can serve declarative image specifications specified in CRDs as container
images. The design for this is outlined in [a public gist][gist].

Currently it focuses on the ad-hoc creation of container images as outlined
below with an example instance available at
[nixery.appspot.com](https://nixery.appspot.com).

This is not an officially supported Google project.

## Ad-hoc container images

Nixery supports building images on-demand based on the *image name*. Every
package that the user intends to include in the image is specified as a path
component of the image name.

The path components refer to top-level keys in `nixpkgs` and are used to build a
container image using Nix's [buildLayeredImage][] functionality.

The special meta-package `shell` provides an image base with many core
components (such as `bash` and `coreutils`) that users commonly expect in
interactive images.

## Usage example

Using the publicly available Nixery instance at `nixery.appspot.com`, one could
retrieve a container image containing `curl` and an interactive shell like this:

```shell
tazjin@tazbox:~$ sudo docker run -ti nixery.appspot.com/shell/curl bash
Unable to find image 'nixery.appspot.com/shell/curl:latest' locally
latest: Pulling from shell/curl
7734b79e1ba1: Already exists
b0d2008d18cd: Pull complete
< ... some layers omitted ...>
Digest: sha256:178270bfe84f74548b6a43347d73524e5c2636875b673675db1547ec427cf302
Status: Downloaded newer image for nixery.appspot.com/shell/curl:latest
bash-4.4# curl --version
curl 7.64.0 (x86_64-pc-linux-gnu) libcurl/7.64.0 OpenSSL/1.0.2q zlib/1.2.11 libssh2/1.8.0 nghttp2/1.35.1
```

## Kubernetes integration (in the future)

**Note**: The Kubernetes integration is not yet implemented.

The basic idea of the Kubernetes integration is to provide a way for users to
specify the contents of a container image as an API object in Kubernetes which
will be transparently built by Nix when the container is started up.

For example, given a resource that looks like this:

```yaml
---
apiVersion: k8s.nixos.org/v1alpha
kind: NixImage
metadata:
  name: curl-and-jq
data:
  tag: v1
  contents:
    - curl
    - jq
    - bash
```

One could create a container that references the `curl-and-jq` image, which will
then be created by Nix when the container image is pulled.

The controller itself runs as a daemonset on every node in the cluster,
providing a host-mounted `/nix/store`-folder for caching purposes.

[Nix]: https://nixos.org/
[gist]: https://gist.github.com/tazjin/08f3d37073b3590aacac424303e6f745
[buildLayeredImage]: https://grahamc.com/blog/nix-and-layered-docker-images
