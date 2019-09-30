package builder

import (
	"cloud.google.com/go/storage"
	"github.com/google/nixery/config"
	"github.com/google/nixery/layers"
)

// State holds the runtime state that is carried around in Nixery and
// passed to builder functions.
type State struct {
	Bucket *storage.BucketHandle
	Cache  LocalCache
	Cfg    config.Config
	Pop    layers.Popularity
}

func NewState(bucket *storage.BucketHandle, cfg config.Config) State {
	return State{
		Bucket: bucket,
		Cfg:    cfg,
		Cache:  NewCache(),
	}
}
