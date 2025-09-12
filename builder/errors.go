package builder

import (
	"container/ring"
	"slices"
	"sync"
)

type BuildError struct {
	Key   string
	Error string
}

type ErrorCache struct {
	size    int
	current *ring.Ring
	errors  map[string]*ring.Ring
	lock    sync.RWMutex
}

func NewErrorCache(size int) *ErrorCache {
	r := ring.New(max(size, 1))
	errors := make(map[string]*ring.Ring)

	return &ErrorCache{
		size:    size,
		current: r,
		errors:  errors,
	}
}

func (ec *ErrorCache) AddError(key, errorMsg string) {
	exists := func() bool {
		ec.lock.RLock()
		defer ec.lock.RUnlock()
		_, ok := ec.errors[key]
		return ok
	}()
	if exists {
		return
	}

	ec.lock.Lock()
	defer ec.lock.Unlock()

	if ec.current.Value != nil {
		if existingError, ok := ec.current.Value.(*BuildError); ok {
			delete(ec.errors, existingError.Key)
		}
	}

	newError := &BuildError{
		Key:   key,
		Error: errorMsg,
	}

	ec.current.Value = newError
	ec.errors[key] = ec.current
	ec.current = ec.current.Next()
}

func (ec *ErrorCache) GetAllErrors() []*BuildError {
	ec.lock.RLock()
	defer ec.lock.RUnlock()

	var result []*BuildError

	ec.current.Do(func(value any) {
		if value != nil {
			if err, ok := value.(*BuildError); ok {
				result = append(result, err)
			}
		}
	})

	slices.Reverse(result) // newest first
	return result
}
