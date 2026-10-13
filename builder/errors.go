package builder

import (
	"slices"
	"sync"
)

type BuildError struct {
	Key   string
	Error string
	Count int
}

type ErrorCache struct {
	size  int
	lock  sync.RWMutex
	order []*BuildError
}

func NewErrorCache(size int) *ErrorCache {
	return &ErrorCache{
		size: max(size, 1),
	}
}

func (ec *ErrorCache) AddError(key, errorMsg string) {
	ec.lock.Lock()
	defer ec.lock.Unlock()

	// known errors get "bumped" to the front if they reoccur
	idx := slices.IndexFunc(ec.order, func(e *BuildError) bool {
		return e.Key == key
	})

	if idx != -1 {
		ec.order[idx].Count++
		ec.order[idx].Error = errorMsg

		if idx != 0 {
			elem := ec.order[idx]
			ec.order = slices.Delete(ec.order, idx, idx+1)
			ec.order = append([]*BuildError{elem}, ec.order...)
		}

		return
	}

	be := &BuildError{Key: key, Error: errorMsg, Count: 1}
	ec.order = append([]*BuildError{be}, ec.order...)

	if len(ec.order) > ec.size {
		ec.order = ec.order[:len(ec.order)-1]
	}
}

func (ec *ErrorCache) GetAllErrors() []*BuildError {
	ec.lock.RLock()
	defer ec.lock.RUnlock()

	return slices.Clone(ec.order)
}
