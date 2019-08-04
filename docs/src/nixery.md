![Nixery](./nixery-logo.png)

------------

Welcome to this instance of [Nixery][]. It provides ad-hoc container images that
contain packages from the [Nix][] package manager. Images with arbitrary
packages can be requested via the image name.

Nix not only provides the packages to include in the images, but also builds the
images themselves by using an interesting layering strategy described in [this
blog post][layers].

## Quick start

Simply pull an image from this registry, separating each package you want
included by a slash:

    docker pull nixery.dev/shell/git/htop

This gives you an image with `git`, `htop` and an interactively configured
shell. You could run it like this:

    docker run -ti nixery.dev/shell/git/htop bash

Each path segment corresponds either to a key in the Nix package set, or a
meta-package that automatically expands to several other packages.

Meta-packages **must** be the first path component if they are used. Currently
the only meta-package is `shell`, which provides a `bash`-shell with interactive
configuration and standard tools like `coreutils`.

**Tip:** When pulling from a private Nixery instance, replace `nixery.dev` in
the above examples with your registry address.

## FAQ

If you have a question that is not answered here, feel free to file an issue on
Github so that we can get it included in this section. The volume of questions
is quite low, thus by definition your question is already frequently asked.

### Where is the source code for this?

Over [on Github][Nixery]. It is licensed under the Apache 2.0 license. Consult
the documentation entries in the sidebar for information on how to set up your
own instance of Nixery.

### Which revision of `nixpkgs` is used for the builds?

The instance at `nixery.dev` tracks a recent NixOS channel, currently NixOS
19.03. The channel is updated several times a day.

Private registries might be configured to track a different channel (such as
`nixos-unstable`) or even track a git repository with custom packages.

### Is this an official Google project?

**No.** Nixery is not officially supported by Google.

### Should I depend on `nixery.dev` in production?

While we appreciate the enthusiasm, if you would like to use Nixery in your
production project we recommend setting up a private instance. The public Nixery
at `nixery.dev` is run on a best-effort basis and we make no guarantees about
availability.

### Who made this?

Nixery was written mostly by [tazjin][].

[grahamc][] authored the image layering strategy. Many people have contributed
to Nix over time, maybe you could become one of them?

[Nixery]: https://github.com/google/nixery
[Nix]: https://nixos.org/nix
[layers]: https://grahamc.com/blog/nix-and-layered-docker-images
[tazjin]: https://github.com/tazjin
[grahamc]: https://github.com/grahamc
