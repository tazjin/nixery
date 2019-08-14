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
	"sync"
	"time"
)

// recencyThreshold is the amount of time that a manifest build will be cached
// for. When using the channel mechanism for retrieving nixpkgs, Nix will
// occasionally re-fetch the channel so things can in fact change while the
// instance is running.
const recencyThreshold = time.Duration(6) * time.Hour

type manifestEntry struct {
	built time.Time
	path  string
}

type void struct{}

type BuildCache struct {
	mmtx   sync.RWMutex
	mcache map[string]manifestEntry

	lmtx   sync.RWMutex
	lcache map[string]void
}

func NewCache() BuildCache {
	return BuildCache{
		mcache: make(map[string]manifestEntry),
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

// Has this manifest been built already? If yes, we can reuse the
// result given that the build happened recently enough.
func (c *BuildCache) manifestFromCache(image *Image) (string, bool) {
	c.mmtx.RLock()

	entry, ok := c.mcache[image.Name+image.Tag]
	c.mmtx.RUnlock()

	if !ok {
		return "", false
	}

	if time.Since(entry.built) > recencyThreshold {
		return "", false
	}

	return entry.path, true
}

// Adds the result of a manifest build to the cache.
func (c *BuildCache) cacheManifest(image *Image, path string) {
	entry := manifestEntry{
		built: time.Now(),
		path:  path,
	}

	c.mmtx.Lock()
	c.mcache[image.Name+image.Tag] = entry
	c.mmtx.Unlock()
}
