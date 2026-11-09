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

// Package logging builds the process-wide slog logger from the log level and format settings.
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger writing to w in the configured format.
func New(w io.Writer, settings Settings) *slog.Logger {
	options := &slog.HandlerOptions{Level: settings.Level}
	if settings.Format == formatText {
		return slog.New(slog.NewTextHandler(w, options))
	}

	return slog.New(slog.NewJSONHandler(w, options))
}

// Discard returns a logger that drops every record.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
