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

// Popcount fetches popularity information for each store path in a
// given Nix channel from the upstream binary cache.
//
// It does this simply by inspecting the narinfo files, rather than
// attempting to deal with instantiation of the binary cache.
//
// This is *significantly* faster than attempting to realise the whole
// channel and then calling `nix path-info` on it.
//
// TODO(tazjin): Persist intermediate results (references for each
// store path) to speed up subsequent runs.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var client http.Client
var pathexp = regexp.MustCompile("/nix/store/([a-z0-9]{32})-(.*)$")
var refsexp = regexp.MustCompile("(?m:^References: (.*)$)")
var refexp = regexp.MustCompile("^([a-z0-9]{32})-(.*)$")

type meta struct {
	name   string
	url    string
	commit string
}

type item struct {
	name string
	hash string
}

func failOn(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
	}
}

func channelMetadata(channel string) meta {
	// This needs an HTTP client that does not follow redirects
	// because the channel URL is used explicitly for other
	// downloads.
	c := http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := c.Get(fmt.Sprintf("https://nixos.org/channels/%s", channel))
	failOn(err, "failed to retrieve channel metadata")

	loc, err := resp.Location()
	failOn(err, "no redirect location given for channel")
	if resp.StatusCode != 302 {
		log.Fatalf("Expected redirect for channel, but received '%s'\n", resp.Status)
	}

	commitResp, err := c.Get(fmt.Sprintf("%s/git-revision", loc.String()))
	failOn(err, "failed to retrieve commit for channel")

	defer commitResp.Body.Close()
	commit, err := ioutil.ReadAll(commitResp.Body)
	failOn(err, "failed to read commit from response")

	return meta{
		name:   channel,
		url:    loc.String(),
		commit: string(commit),
	}
}

func downloadStorePaths(c *meta) []string {
	resp, err := client.Get(fmt.Sprintf("%s/store-paths.xz", c.url))
	failOn(err, "failed to download store-paths.xz")
	defer resp.Body.Close()

	cmd := exec.Command("xzcat")
	stdin, err := cmd.StdinPipe()
	failOn(err, "failed to open xzcat stdin")
	stdout, err := cmd.StdoutPipe()
	failOn(err, "failed to open xzcat stdout")
	defer stdout.Close()

	go func() {
		defer stdin.Close()
		io.Copy(stdin, resp.Body)
	}()

	err = cmd.Start()
	failOn(err, "failed to start xzcat")

	paths, err := ioutil.ReadAll(stdout)
	failOn(err, "failed to read uncompressed store paths")

	err = cmd.Wait()
	failOn(err, "xzcat failed to decompress")

	return strings.Split(string(paths), "\n")
}

func storePathToItem(path string) *item {
	res := pathexp.FindStringSubmatch(path)
	if len(res) != 3 {
		return nil
	}

	return &item{
		hash: res[1],
		name: res[2],
	}
}

func narInfoToRefs(narinfo string) []string {
	all := refsexp.FindAllStringSubmatch(narinfo, 1)

	if len(all) != 1 {
		log.Fatalf("failed to parse narinfo:\n%s\nfound: %v\n", narinfo, all[0])
	}

	if len(all[0]) != 2 {
		// no references found
		return []string{}
	}

	refs := strings.Split(all[0][1], " ")
	for i, s := range refs {
		if s == "" {
			continue
		}

		res := refexp.FindStringSubmatch(s)
		refs[i] = res[2]
	}

	return refs
}

func fetchNarInfo(i *item) (string, error) {
	file, err := ioutil.ReadFile("popcache/" + i.hash)
	if err == nil {
		return string(file), nil
	}

	resp, err := client.Get(fmt.Sprintf("https://cache.nixos.org/%s.narinfo", i.hash))
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	narinfo, err := ioutil.ReadAll(resp.Body)

	// best-effort write the file to the cache
	ioutil.WriteFile("popcache/"+i.hash, narinfo, 0644)

	return string(narinfo), err
}

// downloader starts a worker that takes care of downloading narinfos
// for all paths received from the queue.
//
// If there is no data remaining in the queue, the downloader exits
// and informs the finaliser queue about having exited.
func downloader(queue chan *item, narinfos chan string, downloaders chan struct{}) {
	for i := range queue {
		ni, err := fetchNarInfo(i)
		if err != nil {
			log.Printf("couldn't fetch narinfo for %s: %s\n", i.name, err)
			continue

		}
		narinfos <- ni
	}
	downloaders <- struct{}{}
}

// finaliser counts the number of downloaders that have exited and
// closes the narinfos queue to signal to the counters that no more
// elements will arrive.
func finaliser(count int, downloaders chan struct{}, narinfos chan string) {
	for range downloaders {
		count--
		if count == 0 {
			close(downloaders)
			close(narinfos)
			break
		}
	}
}

func main() {
	if len(os.Args) == 1 {
		log.Fatalf("Nix channel must be specified as first argument")
	}

	err := os.MkdirAll("popcache", 0755)
	if err != nil {
		log.Fatalf("Failed to create 'popcache' directory in current folder: %s\n", err)
	}

	count := 42 // concurrent downloader count
	channel := os.Args[1]
	log.Printf("Fetching metadata for channel '%s'\n", channel)

	meta := channelMetadata(channel)
	log.Printf("Pinned channel '%s' to commit '%s'\n", meta.name, meta.commit)

	paths := downloadStorePaths(&meta)
	log.Printf("Fetching references for %d store paths\n", len(paths))

	// Download paths concurrently and receive their narinfos into
	// a channel. Data is collated centrally into a map and
	// serialised at the /very/ end.
	downloadQueue := make(chan *item, len(paths))
	for _, p := range paths {
		if i := storePathToItem(p); i != nil {
			downloadQueue <- i
		}
	}
	close(downloadQueue)

	// Set up a task tracking channel for parsing & counting
	// narinfos, as well as a coordination channel for signaling
	// that all downloads have finished
	narinfos := make(chan string, 50)
	downloaders := make(chan struct{}, count)
	for i := 0; i < count; i++ {
		go downloader(downloadQueue, narinfos, downloaders)
	}

	go finaliser(count, downloaders, narinfos)

	counts := make(map[string]int)
	for ni := range narinfos {
		refs := narInfoToRefs(ni)
		for _, ref := range refs {
			if ref == "" {
				continue
			}

			counts[ref] += 1
		}
	}

	// Remove all self-references (i.e. packages not referenced by anyone else)
	for k, v := range counts {
		if v == 1 {
			delete(counts, k)
		}
	}

	bytes, _ := json.Marshal(counts)
	outfile := fmt.Sprintf("popularity-%s-%s.json", meta.name, meta.commit)
	err = ioutil.WriteFile(outfile, bytes, 0644)
	if err != nil {
		log.Fatalf("Failed to write output to '%s': %s\n", outfile, err)
	}

	log.Printf("Wrote output to '%s'\n", outfile)
}
