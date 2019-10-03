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
	"io/ioutil"
	"log"
	"sync"

	"github.com/google/nixery/manifest"
)

// LocalCache implements the structure used for local caching of
// manifests and layer uploads.
type LocalCache struct {
	// Manifest cache
	mmtx   sync.RWMutex
	mcache map[string]string

	// Layer cache
	lmtx   sync.RWMutex
	lcache map[string]manifest.Entry
}

func NewCache() LocalCache {
	return LocalCache{
		mcache: make(map[string]string),
		lcache: make(map[string]manifest.Entry),
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

// Retrieve a layer build from the local cache.
func (c *LocalCache) layerFromLocalCache(key string) (*manifest.Entry, bool) {
	c.lmtx.RLock()
	e, ok := c.lcache[key]
	c.lmtx.RUnlock()

	return &e, ok
}

// Add a layer build result to the local cache.
func (c *LocalCache) localCacheLayer(key string, e manifest.Entry) {
	c.lmtx.Lock()
	c.lcache[key] = e
	c.lmtx.Unlock()
}

// Retrieve a manifest from the cache(s). First the local cache is
// checked, then the GCS-bucket cache.
func manifestFromCache(ctx context.Context, s *State, key string) (json.RawMessage, bool) {
	// path, cached := s.Cache.manifestFromLocalCache(key)
	// if cached {
	// 	return path, true
	// }
	// TODO: local cache?

	obj := s.Bucket.Object("manifests/" + key)

	// Probe whether the file exists before trying to fetch it.
	_, err := obj.Attrs(ctx)
	if err != nil {
		return nil, false
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("Failed to retrieve manifest '%s' from cache: %s\n", key, err)
		return nil, false
	}
	defer r.Close()

	m, err := ioutil.ReadAll(r)
	if err != nil {
		log.Printf("Failed to read cached manifest for '%s': %s\n", key, err)
	}

	// TODO: locally cache manifest, but the cache needs to be changed
	log.Printf("Retrieved manifest for sha1:%s from GCS\n", key)
	return json.RawMessage(m), true
}

// Add a manifest to the bucket & local caches
func cacheManifest(ctx context.Context, s *State, key string, m json.RawMessage) {
	// go s.Cache.localCacheManifest(key, path)
	// TODO local cache

	obj := s.Bucket.Object("manifests/" + key)
	w := obj.NewWriter(ctx)
	r := bytes.NewReader([]byte(m))

	size, err := io.Copy(w, r)
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

// Retrieve a layer build from the cache, first checking the local
// cache followed by the bucket cache.
func layerFromCache(ctx context.Context, s *State, key string) (*manifest.Entry, bool) {
	if entry, cached := s.Cache.layerFromLocalCache(key); cached {
		return entry, true
	}

	obj := s.Bucket.Object("builds/" + key)
	_, err := obj.Attrs(ctx)
	if err != nil {
		return nil, false
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("Failed to retrieve layer build '%s' from cache: %s\n", key, err)
		return nil, false
	}
	defer r.Close()

	jb := bytes.NewBuffer([]byte{})
	_, err = io.Copy(jb, r)
	if err != nil {
		log.Printf("Failed to read layer build '%s' from cache: %s\n", key, err)
		return nil, false
	}

	var entry manifest.Entry
	err = json.Unmarshal(jb.Bytes(), &entry)
	if err != nil {
		log.Printf("Failed to unmarshal layer build '%s' from cache: %s\n", key, err)
		return nil, false
	}

	go s.Cache.localCacheLayer(key, entry)
	return &entry, true
}

func cacheLayer(ctx context.Context, s *State, key string, entry manifest.Entry) {
	s.Cache.localCacheLayer(key, entry)

	obj := s.Bucket.Object("builds/" + key)

	j, _ := json.Marshal(&entry)

	w := obj.NewWriter(ctx)

	_, err := io.Copy(w, bytes.NewReader(j))
	if err != nil {
		log.Printf("failed to cache build '%s': %s\n", key, err)
		return
	}
}
