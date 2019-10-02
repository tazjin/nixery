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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"
)

type Build struct {
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
}

// LocalCache implements the structure used for local caching of
// manifests and layer uploads.
type LocalCache struct {
	// Manifest cache
	mmtx   sync.RWMutex
	mcache map[string]string

	// Layer cache
	lmtx   sync.RWMutex
	lcache map[string]Build
}

func NewCache() LocalCache {
	return LocalCache{
		mcache: make(map[string]string),
		lcache: make(map[string]Build),
	}
}

// Retrieve a cached manifest if the build is cacheable and it exists.
func (c *LocalCache) manifestFromLocalCache(key string) (string, bool) {
	c.mmtx.RLock()
	path, ok := c.mcache[key]
	c.mmtx.RUnlock()

	if !ok {
		return "", false
	}

	return path, true
}

// Adds the result of a manifest build to the local cache, if the
// manifest is considered cacheable.
func (c *LocalCache) localCacheManifest(key, path string) {
	c.mmtx.Lock()
	c.mcache[key] = path
	c.mmtx.Unlock()
}

// Retrieve a cached build from the local cache.
func (c *LocalCache) buildFromLocalCache(key string) (*Build, bool) {
	c.lmtx.RLock()
	b, ok := c.lcache[key]
	c.lmtx.RUnlock()

	return &b, ok
}

// Add a build result to the local cache.
func (c *LocalCache) localCacheBuild(key string, b Build) {
	c.lmtx.Lock()
	c.lcache[key] = b
	c.lmtx.Unlock()
}

// Retrieve a manifest from the cache(s). First the local cache is
// checked, then the GCS-bucket cache.
func manifestFromCache(ctx context.Context, s *State, key string) (string, bool) {
	path, cached := s.Cache.manifestFromLocalCache(key)
	if cached {
		return path, true
	}

	obj := s.Bucket.Object("manifests/" + key)

	// Probe whether the file exists before trying to fetch it.
	_, err := obj.Attrs(ctx)
	if err != nil {
		return "", false
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("Failed to retrieve manifest '%s' from cache: %s\n", key, err)
		return "", false
	}
	defer r.Close()

	path = os.TempDir() + "/" + key
	f, _ := os.Create(path)
	defer f.Close()

	_, err = io.Copy(f, r)
	if err != nil {
		log.Printf("Failed to read cached manifest for '%s': %s\n", key, err)
	}

	log.Printf("Retrieved manifest for sha1:%s from GCS\n", key)
	go s.Cache.localCacheManifest(key, path)

	return path, true
}

// Add a manifest to the bucket & local caches
func cacheManifest(ctx context.Context, s *State, key, path string) {
	go s.Cache.localCacheManifest(key, path)

	obj := s.Bucket.Object("manifests/" + key)
	w := obj.NewWriter(ctx)

	f, err := os.Open(path)
	if err != nil {
		log.Printf("failed to open manifest sha1:%s for cache upload: %s\n", key, err)
		return
	}
	defer f.Close()

	size, err := io.Copy(w, f)
	if err != nil {
		log.Printf("failed to cache manifest sha1:%s: %s\n", key, err)
		return
	}

	if err = w.Close(); err != nil {
		log.Printf("failed to cache manifest sha1:%s: %s\n", key, err)
		return
	}

	log.Printf("Cached manifest sha1:%s (%v bytes written)\n", key, size)
}

// Retrieve a build from the cache, first checking the local cache
// followed by the bucket cache.
func buildFromCache(ctx context.Context, s *State, key string) (*Build, bool) {
	build, cached := s.Cache.buildFromLocalCache(key)
	if cached {
		return build, true
	}

	obj := s.Bucket.Object("builds/" + key)
	_, err := obj.Attrs(ctx)
	if err != nil {
		return nil, false
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("Failed to retrieve build '%s' from cache: %s\n", key, err)
		return nil, false
	}
	defer r.Close()

	jb := bytes.NewBuffer([]byte{})
	_, err = io.Copy(jb, r)
	if err != nil {
		log.Printf("Failed to read build '%s' from cache: %s\n", key, err)
		return nil, false
	}

	var b Build
	err = json.Unmarshal(jb.Bytes(), &build)
	if err != nil {
		log.Printf("Failed to unmarshal build '%s' from cache: %s\n", key, err)
		return nil, false
	}

	go s.Cache.localCacheBuild(key, b)
	return &b, true
}

func cacheBuild(ctx context.Context, s *State, key string, build Build) {
	go s.Cache.localCacheBuild(key, build)

	obj := s.Bucket.Object("builds/" + key)

	j, _ := json.Marshal(&build)

	w := obj.NewWriter(ctx)

	_, err := io.Copy(w, bytes.NewReader(j))
	if err != nil {
		log.Printf("failed to cache build '%s': %s\n", key, err)
		return
	}
}
