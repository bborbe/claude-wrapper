// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import stderrors "errors"

// staleReason classifies a failed read for the summary line.
//
// A vault-api outage is the case the field exists for, so it is named exactly;
// every other failure collapses to `read-error` rather than carrying its wrapped
// text onto the line, which would make the field ungreppable and its width vary
// with the error. The full error stays on the `read failed` line above.
func staleReason(err error) string {
	if stderrors.Is(err, ErrVaultAPIUnavailable) {
		return "vault-api-unavailable"
	}
	return "read-error"
}

// rowsStaleField renders ` rows_stale=<reason>`, or "" when the read succeeded.
//
// It sits on the summary line because the `flagged-tick read failed:` line is a
// separate one: a reader who greps ticks sees the summary alone and could not
// otherwise tell a cached run from a healthy one.
func (r FlaggedTickResult) rowsStaleField() string {
	if r.RowsStale == "" {
		return ""
	}
	return " rows_stale=" + r.RowsStale
}
