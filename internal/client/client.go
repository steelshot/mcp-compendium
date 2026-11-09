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

// Package client builds the typed admin API client used by mcpcd's client commands.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ogen-go/ogen/ogenerrors"

	"github.com/steelshot/mcp-compendium/internal/admin"
	"github.com/steelshot/mcp-compendium/internal/admin/gen"
)

// credentials implements gen.SecuritySource, sending whichever credential is configured.
type credentials struct {
	token, tokenFile, username, password string
}

const requestTimeout = 30 * time.Second

var (
	errAmbiguousCredentials = errors.New("set either a bearer token or a username and password, not both")
	errDaemon               = errors.New("daemon refused the request")
)

// New returns an admin API client.
func New(settings Settings) (*gen.Client, error) {
	source := credentials{token: settings.Token, tokenFile: settings.TokenFile, username: settings.Username, password: settings.Password}

	client, err := gen.NewClient(settings.URL.String()+admin.Path, source, gen.WithClient(&http.Client{Timeout: requestTimeout}))
	if err != nil {
		return nil, fmt.Errorf("build admin client: %w", err)
	}

	return client, nil
}

// Describe turns an admin API error into one readable line.
func Describe(err error) error {
	if problem, ok := errors.AsType[*gen.ProblemStatusCode](err); ok {
		detail, _ := problem.Response.Detail.Get()

		return fmt.Errorf("%w: %s (%d): %s", errDaemon, problem.Response.Title, problem.StatusCode, detail)
	}

	return err
}

func (c credentials) Bearer(context.Context, gen.OperationName) (gen.Bearer, error) {
	token := c.token
	if token == "" && c.tokenFile != "" {
		data, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return gen.Bearer{}, fmt.Errorf("read token file: %w", err)
		}

		token = strings.TrimSpace(string(data))
	}

	if token == "" {
		return gen.Bearer{}, ogenerrors.ErrSkipClientSecurity
	}

	return gen.Bearer{Token: token}, nil
}

func (c credentials) Basic(context.Context, gen.OperationName) (gen.Basic, error) {
	if c.username == "" {
		return gen.Basic{}, ogenerrors.ErrSkipClientSecurity
	}

	return gen.Basic{Username: c.username, Password: c.password}, nil
}
