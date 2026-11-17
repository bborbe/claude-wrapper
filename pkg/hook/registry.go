// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

// NewDefaultRegistry returns the guard chain in the order settings.json
// registered the scripts it replaces.
//
// Guards are added here as they are ported. A guard that is not yet in this list
// still runs as its script, so the chain is never partially switched.
func NewDefaultRegistry() *Registry {
	return NewRegistry(
		NewTypoGuard(),
		NewSleepGuard(),
		NewProdDeployGuard(),
		NewNukeShutdownGuard(),
		NewAttributionGuard(),
	)
}
