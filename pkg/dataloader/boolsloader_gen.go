// Code generated by github.com/vektah/dataloaden, DO NOT EDIT.

package dataloader

import (
	"sync"
	"time"

	"github.com/gofrs/uuid"
)

// BoolsLoaderConfig captures the config to create a new BoolsLoader
type BoolsLoaderConfig struct {
	// Fetch is a method that provides the data for the loader
	Fetch func(keys []uuid.UUID) ([]bool, []error)

	// Wait is how long wait before sending a batch
	Wait time.Duration

	// MaxBatch will limit the maximum number of keys to send in one batch, 0 = not limit
	MaxBatch int
}

// NewBoolsLoader creates a new BoolsLoader given a fetch, wait, and maxBatch
func NewBoolsLoader(config BoolsLoaderConfig) *BoolsLoader {
	return &BoolsLoader{
		fetch:    config.Fetch,
		wait:     config.Wait,
		maxBatch: config.MaxBatch,
	}
}

// BoolsLoader batches and caches requests
type BoolsLoader struct {
	// this method provides the data for the loader
	fetch func(keys []uuid.UUID) ([]bool, []error)

	// how long to done before sending a batch
	wait time.Duration

	// this will limit the maximum number of keys to send in one batch, 0 = no limit
	maxBatch int

	// INTERNAL

	// lazily created cache
	cache map[uuid.UUID]bool

	// the current batch. keys will continue to be collected until timeout is hit,
	// then everything will be sent to the fetch method and out to the listeners
	batch *boolsLoaderBatch

	// mutex to prevent races
	mu sync.Mutex
}

type boolsLoaderBatch struct {
	keys    []uuid.UUID
	data    []bool
	error   []error
	closing bool
	done    chan struct{}
}

// Load a bool by key, batching and caching will be applied automatically
func (l *BoolsLoader) Load(key uuid.UUID) (bool, error) {
	return l.LoadThunk(key)()
}

// LoadThunk returns a function that when called will block waiting for a bool.
// This method should be used if you want one goroutine to make requests to many
// different data loaders without blocking until the thunk is called.
func (l *BoolsLoader) LoadThunk(key uuid.UUID) func() (bool, error) {
	l.mu.Lock()
	if it, ok := l.cache[key]; ok {
		l.mu.Unlock()
		return func() (bool, error) {
			return it, nil
		}
	}
	if l.batch == nil {
		l.batch = &boolsLoaderBatch{done: make(chan struct{})}
	}
	batch := l.batch
	pos := batch.keyIndex(l, key)
	l.mu.Unlock()

	return func() (bool, error) {
		<-batch.done

		var data bool
		if pos < len(batch.data) {
			data = batch.data[pos]
		}

		var err error
		// its convenient to be able to return a single error for everything
		if len(batch.error) == 1 {
			err = batch.error[0]
		} else if batch.error != nil {
			err = batch.error[pos]
		}

		if err == nil {
			l.mu.Lock()
			l.unsafeSet(key, data)
			l.mu.Unlock()
		}

		return data, err
	}
}

// LoadAll fetches many keys at once. It will be broken into appropriate sized
// sub batches depending on how the loader is configured
func (l *BoolsLoader) LoadAll(keys []uuid.UUID) ([]bool, []error) {
	results := make([]func() (bool, error), len(keys))

	for i, key := range keys {
		results[i] = l.LoadThunk(key)
	}

	bools := make([]bool, len(keys))
	errors := make([]error, len(keys))
	for i, thunk := range results {
		bools[i], errors[i] = thunk()
	}
	return bools, errors
}

// LoadAllThunk returns a function that when called will block waiting for a bools.
// This method should be used if you want one goroutine to make requests to many
// different data loaders without blocking until the thunk is called.
func (l *BoolsLoader) LoadAllThunk(keys []uuid.UUID) func() ([]bool, []error) {
	results := make([]func() (bool, error), len(keys))
	for i, key := range keys {
		results[i] = l.LoadThunk(key)
	}
	return func() ([]bool, []error) {
		bools := make([]bool, len(keys))
		errors := make([]error, len(keys))
		for i, thunk := range results {
			bools[i], errors[i] = thunk()
		}
		return bools, errors
	}
}

// Prime the cache with the provided key and value. If the key already exists, no change is made
// and false is returned.
// (To forcefully prime the cache, clear the key first with loader.clear(key).prime(key, value).)
func (l *BoolsLoader) Prime(key uuid.UUID, value bool) bool {
	l.mu.Lock()
	var found bool
	if _, found = l.cache[key]; !found {
		l.unsafeSet(key, value)
	}
	l.mu.Unlock()
	return !found
}

// Clear the value at key from the cache, if it exists
func (l *BoolsLoader) Clear(key uuid.UUID) {
	l.mu.Lock()
	delete(l.cache, key)
	l.mu.Unlock()
}

func (l *BoolsLoader) unsafeSet(key uuid.UUID, value bool) {
	if l.cache == nil {
		l.cache = map[uuid.UUID]bool{}
	}
	l.cache[key] = value
}

// keyIndex will return the location of the key in the batch, if its not found
// it will add the key to the batch
func (b *boolsLoaderBatch) keyIndex(l *BoolsLoader, key uuid.UUID) int {
	for i, existingKey := range b.keys {
		if key == existingKey {
			return i
		}
	}

	pos := len(b.keys)
	b.keys = append(b.keys, key)
	if pos == 0 {
		go b.startTimer(l)
	}

	if l.maxBatch != 0 && pos >= l.maxBatch-1 {
		if !b.closing {
			b.closing = true
			l.batch = nil
			go b.end(l)
		}
	}

	return pos
}

func (b *boolsLoaderBatch) startTimer(l *BoolsLoader) {
	time.Sleep(l.wait)
	l.mu.Lock()

	// we must have hit a batch limit and are already finalizing this batch
	if b.closing {
		l.mu.Unlock()
		return
	}

	l.batch = nil
	l.mu.Unlock()

	b.end(l)
}

func (b *boolsLoaderBatch) end(l *BoolsLoader) {
	b.data, b.error = l.fetch(b.keys)
	close(b.done)
}
