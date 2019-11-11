// Copyright 2019 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not
// use this file except in compliance with the License. You may obtain a copy of
// the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations under
// the License.
package builder

import (
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"testing"
)

var ignoreArch = cmpopts.IgnoreFields(Image{}, "Arch")

func TestImageFromNameSimple(t *testing.T) {
	image := ImageFromName("hello", "latest")
	expected := Image{
		Name: "hello",
		Tag:  "latest",
		Packages: []string{
			"cacert",
			"hello",
			"iana-etc",
		},
	}

	if diff := cmp.Diff(expected, image, ignoreArch); diff != "" {
		t.Fatalf("Image(\"hello\", \"latest\") mismatch:\n%s", diff)
	}
}

func TestImageFromNameMultiple(t *testing.T) {
	image := ImageFromName("hello/git/htop", "latest")
	expected := Image{
		Name: "git/hello/htop",
		Tag:  "latest",
		Packages: []string{
			"cacert",
			"git",
			"hello",
			"htop",
			"iana-etc",
		},
	}

	if diff := cmp.Diff(expected, image, ignoreArch); diff != "" {
		t.Fatalf("Image(\"hello/git/htop\", \"latest\") mismatch:\n%s", diff)
	}
}

func TestImageFromNameShell(t *testing.T) {
	image := ImageFromName("shell", "latest")
	expected := Image{
		Name: "shell",
		Tag:  "latest",
		Packages: []string{
			"bashInteractive",
			"cacert",
			"coreutils",
			"iana-etc",
			"moreutils",
			"nano",
		},
	}

	if diff := cmp.Diff(expected, image, ignoreArch); diff != "" {
		t.Fatalf("Image(\"shell\", \"latest\") mismatch:\n%s", diff)
	}
}

func TestImageFromNameShellMultiple(t *testing.T) {
	image := ImageFromName("shell/htop", "latest")
	expected := Image{
		Name: "htop/shell",
		Tag:  "latest",
		Packages: []string{
			"bashInteractive",
			"cacert",
			"coreutils",
			"htop",
			"iana-etc",
			"moreutils",
			"nano",
		},
	}

	if diff := cmp.Diff(expected, image, ignoreArch); diff != "" {
		t.Fatalf("Image(\"shell/htop\", \"latest\") mismatch:\n%s", diff)
	}
}

func TestImageFromNameShellArm64(t *testing.T) {
	image := ImageFromName("shell/arm64", "latest")
	expected := Image{
		Name: "arm64/shell",
		Tag:  "latest",
		Packages: []string{
			"bashInteractive",
			"cacert",
			"coreutils",
			"iana-etc",
			"moreutils",
			"nano",
		},
	}

	if diff := cmp.Diff(expected, image, ignoreArch); diff != "" {
		t.Fatalf("Image(\"shell/arm64\", \"latest\") mismatch:\n%s", diff)
	}

	if image.Arch.imageArch != "arm64" {
		t.Fatal("Image(\"shell/arm64\"): Expected arch arm64")
	}
}
