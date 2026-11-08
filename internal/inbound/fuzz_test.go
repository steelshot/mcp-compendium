/*
 * Copyright (c) 2026 Džiugas Eiva
 *
 * SPDX-License-Identifier: AGPL-3.0-only
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package inbound

import (
	"encoding/base64"
	"testing"
)

// FuzzUnverifiedIssuer checks that arbitrary bearer tokens never panic while their issuer is read for routing.
func FuzzUnverifiedIssuer(f *testing.F) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss": "https://issuer.example", "sub": "x"}`))
	for _, seed := range []string{"header." + payload + ".signature", "a.b.c", "no-dots", ".", "..", "a.!!!.c", ""} {
		f.Add(seed)
	}

	f.Fuzz(func(_ *testing.T, token string) {
		_ = unverifiedIssuer(token)
	})
}
