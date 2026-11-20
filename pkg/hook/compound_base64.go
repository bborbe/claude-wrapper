// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import "strings"

// base64DecodeFlags select decoding; base64ValueFlags take a numeric value.
var (
	base64DecodeFlags = map[string]bool{"-d": true, "-D": true, "--decode": true}
	base64ValueFlags  = map[string]bool{"-b": true, "--break": true}
)

// Base64DecodeAllows reports whether the invocation is `base64 -d|-D|--decode` over
// stdin — a pure transform. `-o/--output` writes a file past the redirect check and
// `-i/--input` or an operand names one, so each is refused with any unknown flag.
func Base64DecodeAllows(values []string) bool {
	decode := false
	for i := 0; i < len(values); i++ {
		value := values[i]
		switch {
		case base64DecodeFlags[value]:
			decode = true
		case base64ValueFlags[value]:
			if i+1 >= len(values) || !allDigits(values[i+1]) {
				return false
			}
			i++
		case strings.HasPrefix(value, "--") && strings.Contains(value, "="):
			name, number, _ := strings.Cut(value, "=")
			if !base64ValueFlags[name] || !allDigits(number) {
				return false
			}
		default:
			return false
		}
	}
	return decode
}
