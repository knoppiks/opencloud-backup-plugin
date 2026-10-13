package statetest

import (
	"context"

	"github.com/knoppiks/opencloud-backup-plugin/internal/state"
)

// HonoursContext wraps st so every call fails with the context's error once
// the context is done, as a real backend's network call would. The memory
// store ignores its context; tests that care whether a write outlives a
// cancelled caller use this.
func HonoursContext(st state.Store) state.Store { return ctxStore{st} }

type ctxStore struct{ inner state.Store }

func (s ctxStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.inner.Get(ctx, key)
}

func (s ctxStore) Create(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.inner.Create(ctx, key, value)
}

func (s ctxStore) Replace(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.inner.Replace(ctx, key, value)
}

func (s ctxStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.inner.Delete(ctx, key)
}

func (s ctxStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.inner.List(ctx, prefix)
}
