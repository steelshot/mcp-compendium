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

package downstream

import (
	"errors"
	"fmt"
	"time"
)

// Error classifies a failed downstream operation so callers can tell agents what to do next.
type Error struct {
	Kind   ErrorKind
	Server string
	// RetryAfter is set when the server is cooling down after a failure.
	RetryAfter time.Duration
	// MayHaveRun is set when a call failed after it was sent and was not retried, so its side effects are unknown.
	MayHaveRun bool
	Err        error
}

// ErrorKind is the category of a downstream failure.
type ErrorKind string

// Error kinds.
const (
	KindUnknownServer ErrorKind = "unknown_server"
	KindUnauthorized  ErrorKind = "unauthorized"
	KindUnavailable   ErrorKind = "unavailable"
	KindTimeout       ErrorKind = "timeout"
)

var (
	errUnauthorized     = errors.New("the server requires authorization")
	errNotConnected     = errors.New("not connected")
	errUnknownServer    = errors.New("unknown server")
	errStartupTimeout   = errors.New("did not finish connecting before the startup timeout")
	errExecutionTimeout = errors.New("execution timeout")
	errTooManySessions  = errors.New("too many client sessions for an isolated server")
)

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.Server, e.Kind, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}
