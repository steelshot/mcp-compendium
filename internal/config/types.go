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

package config

import (
	"net/url"
	"slices"
	"time"
)

// Config is the merged, validated configuration.
type Config struct {
	// Servers are sorted by name.
	Servers []*Server
	Auth    Auth
	// Sources lists the files the configuration was read from, in merge order.
	Sources []string
}

// Server is one downstream MCP server.
type Server struct {
	Name        string
	Transport   Transport
	Enabled     bool
	Description string
	// Stdio transport.
	Command string
	Args    []string
	Env     map[string]string
	Dir     string
	// HTTP and SSE transports.
	URL     *url.URL
	Headers map[string]string
	OAuth   *OAuth

	Tools     ToolFilter
	Limits    Limits
	Isolation Isolation
	// Source is the file that defined the server.
	Source string
}

// ToolFilter selects which downstream tools are exposed.
type ToolFilter struct {
	Include  []string
	Exclude  []string
	ReadOnly bool
}

// Limits overrides daemon-wide limits for one server; zero values mean "use the daemon default".
type Limits struct {
	MaxResultBytes   int
	StartupTimeout   time.Duration
	ExecutionTimeout time.Duration
}

// OAuth configures a pre-registered OAuth client for a downstream server. Without it the daemon registers a client
// dynamically when the server requires authorization.
type OAuth struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Auth configures inbound authentication.
type Auth struct {
	AllowAnonymous bool
	Providers      []*Provider
}

// Provider is one inbound authentication provider.
type Provider struct {
	Name   string
	Type   ProviderType
	Users  []BasicUser
	Tokens []BearerToken
	JWT    *JWTProvider
	Source string
}

// BasicUser is a username and the bcrypt hash of its password, accepted by a basic provider.
type BasicUser struct {
	Username     string
	PasswordHash string
	Roles        []Role
}

// BearerToken is a static token accepted by a bearer provider.
type BearerToken struct {
	Subject string
	Token   string
	Roles   []Role
}

// JWTProvider validates JWTs issued by an OIDC-compatible issuer, including Kubernetes service account tokens.
type JWTProvider struct {
	Issuer       string
	Audiences    []string
	SubjectClaim string
	JWKS         JWKS
	Grants       []Grant
}

// JWKS configures how signing keys are fetched.
type JWKS struct {
	URL             string
	CAFile          string
	BearerTokenFile string
}

// Grant assigns roles to tokens whose claims match every pattern.
type Grant struct {
	Match map[string]string
	Roles []Role
}

// Transport is a downstream transport type.
type Transport string

// Isolation selects whether a downstream connection is shared by every client session.
type Isolation string

// ProviderType is an inbound authentication provider type.
type ProviderType string

// Role is an inbound authorization role.
type Role string

// Recognized transports, isolation modes, provider types and roles.
const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"

	IsolationShared  Isolation = "shared"
	IsolationSession Isolation = "session"

	ProviderBasic  ProviderType = "basic"
	ProviderBearer ProviderType = "bearer"
	ProviderJWT    ProviderType = "jwt"

	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Server returns the server named name, or nil.
func (c *Config) Server(name string) *Server {
	index, found := slices.BinarySearchFunc(c.Servers, name, func(server *Server, target string) int {
		switch {
		case server.Name < target:
			return -1
		case server.Name > target:
			return 1
		default:
			return 0
		}
	})
	if !found {
		return nil
	}

	return c.Servers[index]
}

// EnabledServers returns the enabled servers in name order.
func (c *Config) EnabledServers() []*Server {
	enabled := make([]*Server, 0, len(c.Servers))
	for _, server := range c.Servers {
		if server.Enabled {
			enabled = append(enabled, server)
		}
	}

	return enabled
}
