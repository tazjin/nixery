# Under the hood

This page serves as a quick explanation of what happens under-the-hood when an
image is requested from Nixery.

<!-- markdown-toc start - Don't edit this section. Run M-x markdown-toc-refresh-toc -->

- [1. The image manifest is requested](#1-the-image-manifest-is-requested)
- [2. Nix builds the image](#2-nix-builds-the-image)
- [3. Layers are uploaded to Nixery's storage](#3-layers-are-uploaded-to-nixerys-storage)
- [4. The image manifest is sent back](#4-the-image-manifest-is-sent-back)
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

## 2. Nix builds the image

Using the parameters above, Nix imports the package set and begins by mapping
the image names to attributes in the package set.

A special case during this process is packages with uppercase characters in
their name, for example anything under `haskellPackages`. The registry protocol
does not allow uppercase characters, so the Nix code will translate something
like `haskellpackages` (lowercased) to the correct attribute name.

After identifying all contents, Nix determines the contents of each layer while
optimising for the best possible cache efficiency (see the [layering design
doc][] for details).

Finally it builds each layer, assembles the image manifest as JSON structure,
and yields this manifest back to the web server.

*Note:* While this step is running (which can take some time in the case of
large first-time image builds), the registry client is left hanging waiting for
an HTTP response. Unfortunately the registry protocol does not allow for any
feedback back to the user at this point, so from the user's perspective things
just ... hang, for a moment.

## 3. Layers are uploaded to Nixery's storage

Nixery inspects the returned manifest and uploads each layer to the configured
[Google Cloud Storage][gcs] bucket. To avoid unnecessary uploading, it will
first check whether layers are already present in the bucket and - just to be
safe - compare their MD5-hashes against what was built.

## 4. The image manifest is sent back

If everything went well at this point, Nixery responds to the registry client
with the image manifest.

The client now inspects the manifest and basically sees a list of SHA256-hashes,
each corresponding to one layer of the image. Most clients will now consult
their local layer storage and determine which layers they are missing.

Each of the missing layers is then requested from Nixery.

## 5. Image layers are requested

For each image layer that it needs to retrieve, the registry client assembles a
request that looks like this:

`GET /v2/${imageName}/blob/sha256:${layerHash}`

Nixery receives these requests and *rewrites* them to Google Cloud Storage URLs,
responding with an `HTTP 303 See Other` status code and the actual download URL
of the layer.

Nixery supports using private buckets which are not generally world-readable, in
which case [signed URLs][] are constructed using a private key. These allow the
registry client to download each layer without needing to care about how the
underlying authentication works.

---------

That's it. After these five steps the registry client has retrieved all it needs
to run the image produced by Nixery.

[gcs]: https://cloud.google.com/storage/
[signed URLs]: https://cloud.google.com/storage/docs/access-control/signed-urls
[layering design doc]: https://storage.googleapis.com/nixdoc/nixery-layers.html
