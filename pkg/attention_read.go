// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bborbe/errors"
)

// readJSONArray reads one JSON array from attention-controller.
//
// One helper rather than two near-identical readers: the ledger and the
// heartbeat store are served by the same process, answer the same way, and
// differ only in path and element type. The rules they share are the ones worth
// stating once — only a 200 is a read (a 404 belongs to the per-id routes and is
// not an answer to a LIST question), and a decode failure is "could not read
// it", never an empty result.
func readJSONArray(
	ctx context.Context,
	httpClient *http.Client,
	baseURL string,
	path string,
	out any,
) error {
	if baseURL == "" {
		return errors.Errorf(ctx, "no attention-controller endpoint configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return errors.Wrapf(ctx, err, "build %s request failed", path)
	}
	request.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(request)
	if err != nil {
		return errors.Wrapf(ctx, err, "read %s failed", path)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.Errorf(ctx, "%s read answered %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return errors.Wrapf(ctx, err, "decode %s failed", path)
	}
	return nil
}
