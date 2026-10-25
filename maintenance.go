package main

import "context"

// Internal maintenance never starts a daemon or negotiates an older protocol.
// The daemon checks emptiness atomically in its registry before shutting down.
func shutdownEmptyRuntime() error {
	w, err := openWorkspace(context.Background(), defaultConfig(), false)
	if err != nil {
		return err
	}
	defer w.close()
	_, err = w.call(packet{Type: "shutdown-empty"})
	return err
}
