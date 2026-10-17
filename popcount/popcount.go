// Copyright The TVL Contributors
// SPDX-License-Identifier: Apache-2.0

// Popcount fetches popularity information for each store path in a
// Nix channel from the upstream binary cache.
//
// It does this simply by inspecting the narinfo files, rather than
// attempting to deal with instantiation of the binary cache.
//
// This is *significantly* faster than attempting to realise the whole
// channel and then calling `nix path-info` on it.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

type item struct {
	name string
	hash string
}

func failOn(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
	}
}

// Resolves a channel URL (e.g. from //third_party/sources) to the
// release directory containing store-paths.xz. URLs pointing directly
// at a file inside the directory (such as nixexprs.tar.xz) are trimmed
// to that directory.
func channelURL(url string) string {
	if !strings.HasPrefix(url, "http") {
		log.Fatalf("expected a channel URL, got '%s'", url)
	}

	return url[:strings.LastIndex(url, "/")+1]
}

func downloadStorePaths(c string) []string {
	resp, err := client.Get(c + "store-paths.xz")
	failOn(err, "failed to download store-paths.xz")
	defer resp.Body.Close()

	paths, err := io.ReadAll(resp.Body)
	failOn(err, "failed to download store-paths.xz")
	if resp.StatusCode != 200 {
		log.Fatalf("failed to download store-paths.xz: %s (%s)", string(paths), resp.Status)
	}

	// decompress via xzcat, to avoid a decompression dependency
	cmd := exec.Command("xzcat")
	stdin, err := cmd.StdinPipe()
	failOn(err, "failed to open xzcat stdin")
	stdout, err := cmd.StdoutPipe()
	failOn(err, "failed to open xzcat stdout")
	defer stdout.Close()

	go func() {
		defer stdin.Close()
		io.Copy(stdin, bytes.NewReader(paths))
	}()

	err = cmd.Start()
	failOn(err, "failed to start xzcat")

	decompressed, err := io.ReadAll(stdout)
	failOn(err, "failed to read uncompressed store paths")

	err = cmd.Wait()
	failOn(err, "xzcat failed to decompress")

	return strings.Split(string(decompressed), "\n")
}

func storePathToItem(p string) *item {
	res := pathexp.FindStringSubmatch(p)
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

	if len(all) != 1 || len(all[0]) != 2 {
		// no references found
		return []string{}
	}

	refs := strings.Split(all[0][1], " ")
	for i, s := range refs {
		if s == "" {
			refs[i] = ""
			continue
		}

		res := refexp.FindStringSubmatch(s)
		refs[i] = res[2]
	}

	return refs
}

func fetchNarInfo(i *item, cache string) (string, error) {
	file, err := os.ReadFile(cache + "/" + i.hash)
	if err == nil {
		return string(file), nil
	}

	resp, err := client.Get(fmt.Sprintf("https://cache.nixos.org/%s.narinfo", i.hash))
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	narinfo, err := io.ReadAll(resp.Body)

	// best-effort write the file to the cache
	os.WriteFile(cache+"/"+i.hash, narinfo, 0644)

	return string(narinfo), err
}

// downloader starts a worker that takes care of downloading narinfos
// for all paths received from the queue.
//
// If there is no data remaining in the queue, the downloader exits
// and informs the finaliser queue about having exited.
func downloader(queue chan *item, narinfos chan string, downloaders chan struct{}, cache string) {
	for i := range queue {
		ni, err := fetchNarInfo(i, cache)
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
	cache := flag.String("cache", "popcache", "directory for caching downloaded narinfos")
	flag.Parse()

	if flag.NArg() != 1 {
		log.Fatalf("usage: popcount [-cache dir] <channel-url>")
	}

	url := channelURL(flag.Arg(0))
	log.Printf("Counting package popularity for channel at %s\n", url)

	err := os.MkdirAll(*cache, 0755)
	failOn(err, "failed to create cache directory")

	paths := downloadStorePaths(url)
	log.Printf("Fetching references for %d store paths\n", len(paths))

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
	count := 42 // concurrent downloader count
	narinfos := make(chan string, 50)
	downloaders := make(chan struct{}, count)
	for i := 0; i < count; i++ {
		go downloader(downloadQueue, narinfos, downloaders, *cache)
	}

	go finaliser(count, downloaders, narinfos)

	counts := make(map[string]int)
	processed := 0
	for ni := range narinfos {
		if processed++; processed%10000 == 0 {
			log.Printf("processed %d/%d narinfos\n", processed, len(paths))
		}

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

	log.Printf("counted references of %d packages, %d have more than one\n", processed, len(counts))

	bytes, err := json.Marshal(counts)
	failOn(err, "failed to marshal popularity data")

	_, err = os.Stdout.Write(bytes)
	failOn(err, "failed to write popularity data")
}
