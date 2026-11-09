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

package oauth

import (
	"fmt"
	"html"
	"net/http"
)

const pageTemplate = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem}</style>
</head><body><h1>%s</h1><p>%s</p></body></html>`

// BrowserHandler serves the sign-in links sent to agents, which redirect to the authorization server, and the
// authorization server's redirect back to the daemon. Neither path takes inbound credentials: the unguessable login ID
// and OAuth state are the capabilities.
func (m *Manager) BrowserHandler(loginPath, callbackPath string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET "+loginPath+"{id}", func(w http.ResponseWriter, r *http.Request) {
		login, ok := m.Pending(r.PathValue("id"))
		if !ok {
			writePage(w, http.StatusNotFound, "Link expired", "This sign-in link has expired or was already used. Ask the agent to try again.")

			return
		}

		secureHeaders(w)
		http.Redirect(w, r, login.AuthorizationURL, http.StatusFound)
	})

	mux.HandleFunc("GET "+callbackPath, func(w http.ResponseWriter, r *http.Request) {
		login, err := m.CompleteRedirect(r.Context(), r.URL.Query())
		if err != nil {
			writePage(w, http.StatusBadRequest, "Sign-in failed", err.Error())

			return
		}

		writePage(w, http.StatusOK, "Signed in", "The daemon can now use "+login.Server+". You can close this tab and return to the agent.")
	})

	return mux
}

func writePage(w http.ResponseWriter, status int, title, message string) {
	secureHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, pageTemplate, html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}

// secureHeaders keeps the authorization code in the callback URL out of caches, referrers and frames.
func secureHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
}
