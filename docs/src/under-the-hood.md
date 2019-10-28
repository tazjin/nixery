# Under the hood

This page serves as a quick explanation of what happens under-the-hood when an
image is requested from Nixery.

<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->

- [1. The image manifest is requested](#1-the-image-manifest-is-requested)
- [2. Nix fetches and prepares image content](#2-nix-fetches-and-prepares-image-content)
- [3. Layers are grouped, created, hashed, and persisted](#3-layers-are-grouped-created-hashed-and-persisted)
- [4. The manifest is assembled and returned to the client](#4-the-manifest-is-assembled-and-returned-to-the-client)
- [5. Image layers are requested](#5-image-layers-are-requested)

<!-- markdown-toc end -->

--------

## 1. The image manifest is requested

When container registry clients such as Docker pull an image, the first thing
they do is ask for the image manifest. This is a JSON document describing which
layers are contained in an image, as well as some additional auxiliary
information.

This request is of the form `GET /v2/$imageName/manifests/$imageTag`.

Nixery receives this request and begins by splitting the image name into its
path components and substituting meta-packages (such as `shell`) for their
contents.

For example, requesting `shell/htop/git` results in Nixery expanding the image
name to `["bashInteractive", "coreutils", "htop", "git"]`.

If Nixery is configured with a private Nix repository, it also looks at the
image tag and substitutes `latest` with `master`.

It then invokes Nix with three parameters:

1. image contents (as above)
2. image tag
3. configured package set source

## 2. Nix fetches and prepares image content

Using the parameters above, Nix imports the package set and begins by mapping
the image names to attributes in the package set.

A special case during this process is packages with uppercase characters in
their name, for example anything under `haskellPackages`. The registry protocol
does not allow uppercase characters, so the Nix code will translate something
like `haskellpackages` (lowercased) to the correct attribute name.

After identifying all contents, Nix uses the `symlinkJoin` function to
create a special layer with the "symlink farm" required to let the
image function like a normal disk image.

Nix then returns information about the image contents as well as the
location of the special layer to Nixery.

## 3. Layers are grouped, created, hashed, and persisted

With the information received from Nix, Nixery determines the contents
of each layer while optimising for the best possible cache efficiency
(see the [layering design doc][] for details).

With the grouped layers, Nixery then begins to create compressed
tarballs with all required contents for each layer. As these tarballs
are being created, they are simultaneously being hashed (as the image
manifest must contain the content-hashes of all layers) and persisted
to storage.

Storage can be either a remote [Google Cloud Storage][gcs] bucket, or
a local filesystem path.

During this step, Nixery checks its build cache (see [Caching][]) to
determine whether a layer needs to be built or is already cached from
a previous build.

*Note:* While this step is running (which can take some time in the case of
large first-time image builds), the registry client is left hanging waiting for
an HTTP response. Unfortunately the registry protocol does not allow for any
feedback back to the user at this point, so from the user's perspective things
just ... hang, for a moment.

## 4. The manifest is assembled and returned to the client

Once armed with the hashes of all required layers, Nixery assembles
the OCI Container Image manifest which describes the structure of the
built image and names all of its layers by their content hash.

This manifest is returned to the client.

## 5. Image layers are requested

The client now inspects the manifest and determines which of the
layers it is currently missing based on their content hashes. Note
that different container runtimes will handle this differently, and in
the case of certain engine and storage driver combinations (e.g.
Docker with OverlayFS) layers might be downloaded again even if they
are already present.

For each of the missing layers, the client now issues a request to
Nixery that looks like this:

`GET /v2/${imageName}/blob/sha256:${layerHash}`

Nixery receives these requests and handles them based on the
configured storage backend.

If the storage backend is GCS, it *redirects* them to Google Cloud
Storage URLs, responding with an `HTTP 303 See Other` status code and
the actual download URL of the layer.

Nixery supports using private buckets which are not generally world-readable, in
which case [signed URLs][] are constructed using a private key. These allow the
registry client to download each layer without needing to care about how the
underlying authentication works.

If the storage backend is the local filesystem, Nixery will attempt to
serve the layer back to the client from disk.

---------

That's it. After these five steps the registry client has retrieved all it needs
to run the image produced by Nixery.

[gcs]: https://cloud.google.com/storage/
[signed URLs]: https://cloud.google.com/storage/docs/access-control/signed-urls
[layering design doc]: https://storage.googleapis.com/nixdoc/nixery-layers.html
