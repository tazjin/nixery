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
	"github.com/google/nixery/config"
	"sync"
)

type void struct{}

type BuildCache struct {
	mmtx   sync.RWMutex
	mcache map[string]string

	lmtx   sync.RWMutex
	lcache map[string]void
}

func NewCache() BuildCache {
	return BuildCache{
		mcache: make(map[string]string),
		lcache: make(map[string]void),
	}
}

// Has this layer hash already been seen by this Nixery instance? If
// yes, we can skip upload checking and such because it has already
// been done.
func (c *BuildCache) hasSeenLayer(hash string) bool {
	c.lmtx.RLock()
	defer c.lmtx.RUnlock()
	_, seen := c.lcache[hash]
	return seen
}

// Layer has now been seen and should be stored.
func (c *BuildCache) sawLayer(hash string) {
	c.lmtx.Lock()
	defer c.lmtx.Unlock()
	c.lcache[hash] = void{}
}

// Retrieve a cached manifest if the build is cacheable and it exists.
func (c *BuildCache) manifestFromCache(src config.PkgSource, image *Image) (string, bool) {
	key := src.CacheKey(image.Packages, image.Tag)
	if key == "" {
		return "", false
	}

	c.mmtx.RLock()
	path, ok := c.mcache[key]
	c.mmtx.RUnlock()

	if !ok {
		return "", false
	}

	return path, true
}

// Adds the result of a manifest build to the cache, if the manifest
// is considered cacheable.
func (c *BuildCache) cacheManifest(src config.PkgSource, image *Image, path string) {
	key := src.CacheKey(image.Packages, image.Tag)
	if key == "" {
		return
	}

	c.mmtx.Lock()
	c.mcache[key] = path
	c.mmtx.Unlock()
}
