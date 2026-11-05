package cli

import (
	"context"
	"errors"
	"os"

	"github.com/darron/fbhook/storage"
	"github.com/darron/fbhook/storage/gcs"
	"github.com/darron/fbhook/storage/tigris"
)

// storageFromEnv selects one adapter for all application state, including policy.
// The returned cleanup is called after both the server and worker have stopped.
func storageFromEnv(ctx context.Context) (storage.Store, func() error, error) {
	switch os.Getenv("FBHOOK_STORAGE_BACKEND") {
	case "", "tigris":
		store, err := tigris.New(ctx, tigris.FromEnv())
		return store, func() error { return nil }, err
	case "gcs":
		store, err := gcs.New(ctx, gcs.FromEnv())
		if err != nil {
			return nil, nil, err
		}
		return store, store.Close, nil
	default:
		return nil, nil, errors.New("FBHOOK_STORAGE_BACKEND must be tigris or gcs")
	}
}
