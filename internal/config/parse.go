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
	"regexp"
	"slices"
	"strings"

	"github.com/gobwas/glob"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/http/httpguts"
)

// fragment is the content of one configuration file before merging.
type fragment struct {
	source         string
	servers        []*Server
	providers      []*Provider
	allowAnonymous *bool
}

const (
	// MinBcryptCost is the lowest bcrypt cost accepted for passwordHash; lower costs make guessing a leaked hash cheap.
	MinBcryptCost    = bcrypt.DefaultCost
	bcryptHashLength = 60
	minTokenLength   = 32
	minResultBytes   = 1024
	defaultSubject   = "sub"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func parseFragment(source string, tree map[string]any, report problems) fragment {
	root := newObject("", tree, report)
	root.ignore("$schema")

	result := fragment{source: source}

	if servers := root.child("servers"); servers != nil {
		names := make([]string, 0, len(servers.values))
		for name := range servers.values {
			names = append(names, name)
		}

		slices.Sort(names)

		for _, name := range names {
			if server := parseServer(servers, name, source); server != nil {
				result.servers = append(result.servers, server)
			}
		}
	}

	if auth := root.child("auth"); auth != nil {
		if auth.has("allowAnonymous") {
			allow := auth.boolean("allowAnonymous", false)
			result.allowAnonymous = &allow
		}

		for _, provider := range auth.children("providers") {
			if parsed := parseProvider(provider, source); parsed != nil {
				result.providers = append(result.providers, parsed)
			}
		}

		auth.finish()
	}

	root.finish()

	return result
}

func parseServer(servers *object, name, source string) *Server {
	entry := servers.child(name)
	if entry == nil {
		return nil
	}

	if !namePattern.MatchString(name) {
		entry.report.add(entry.path, "server names must match %s", namePattern)
	}

	server := &Server{
		Name:        name,
		Transport:   Transport(entry.requiredStr("type")),
		Enabled:     entry.boolean("enabled", true),
		Description: strings.TrimSpace(entry.str("description")),
		Isolation:   IsolationShared,
		Source:      source,
	}

	switch server.Transport {
	case TransportStdio:
		parseStdio(entry, server)
	case TransportHTTP, TransportSSE:
		parseRemote(entry, server)
	default:
		if server.Transport != "" {
			entry.report.add(joinKey(entry.path, "type"), "must be stdio, http or sse, got %q", server.Transport)
		}

		entry.ignore("command", "args", "env", "cwd", "url", "headers", "oauth")
	}

	if tools := entry.child("tools"); tools != nil {
		server.Tools = ToolFilter{Include: globs(tools, "include"), Exclude: globs(tools, "exclude"), ReadOnly: tools.boolean("readOnly", false)}
		tools.finish()
	}

	if limits := entry.child("limits"); limits != nil {
		server.Limits = Limits{
			MaxResultBytes:   limits.integer("maxResultBytes"),
			StartupTimeout:   limits.duration("startupTimeout"),
			ExecutionTimeout: limits.duration("executionTimeout"),
		}
		if server.Limits.MaxResultBytes != 0 && server.Limits.MaxResultBytes < minResultBytes {
			limits.report.add(joinKey(limits.path, "maxResultBytes"), "must be at least %d", minResultBytes)
		}

		limits.finish()
	}

	if entry.has("isolation") {
		server.Isolation = Isolation(entry.str("isolation"))
		if server.Isolation != IsolationShared && server.Isolation != IsolationSession {
			entry.report.add(joinKey(entry.path, "isolation"), "must be shared or session, got %q", server.Isolation)
		}
	}

	entry.finish()

	return server
}

func parseStdio(entry *object, server *Server) {
	server.Command = entry.requiredStr("command")
	server.Args = entry.strs("args")
	server.Env = entry.strMap("env")
	server.Dir = entry.str("cwd")

	for name := range server.Env {
		if !validEnvName(name) {
			entry.report.add(joinKey(joinKey(entry.path, "env"), name), "is not a valid environment variable name")
		}
	}

	entry.forbid("for type stdio", "url", "headers", "oauth")
}

func parseRemote(entry *object, server *Server) {
	raw := entry.requiredStr("url")
	if raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			entry.report.add(joinKey(entry.path, "url"), "must be an absolute http or https URL")
		} else {
			server.URL = parsed
		}
	}

	server.Headers = entry.strMap("headers")
	for name, value := range server.Headers {
		switch {
		case !httpguts.ValidHeaderFieldName(name):
			entry.report.add(joinKey(joinKey(entry.path, "headers"), name), "is not a valid HTTP header name")
		case !httpguts.ValidHeaderFieldValue(value):
			// The value may be a resolved secret, so it is not echoed.
			entry.report.add(joinKey(joinKey(entry.path, "headers"), name), "contains characters not allowed in an HTTP header value")
		}
	}

	if oauth := entry.child("oauth"); oauth != nil {
		server.OAuth = &OAuth{ClientID: oauth.str("clientId"), ClientSecret: oauth.str("clientSecret"), Scopes: oauth.strs("scopes")}
		if server.OAuth.ClientSecret != "" && server.OAuth.ClientID == "" {
			oauth.report.add(joinKey(oauth.path, "clientSecret"), "requires clientId")
		}

		oauth.finish()
	}

	entry.forbid("for type "+string(server.Transport), "command", "args", "env", "cwd")
}

func parseProvider(entry *object, source string) *Provider {
	provider := &Provider{Name: entry.requiredStr("name"), Type: ProviderType(entry.requiredStr("type")), Source: source}
	if provider.Name != "" && !namePattern.MatchString(provider.Name) {
		entry.report.add(joinKey(entry.path, "name"), "must match %s", namePattern)
	}

	switch provider.Type {
	case ProviderBasic:
		provider.Users = parseUsers(entry)
	case ProviderBearer:
		provider.Tokens = parseTokens(entry)
	case ProviderJWT:
		provider.JWT = parseJWT(entry)
	case "":
	default:
		entry.report.add(joinKey(entry.path, "type"), "must be basic, bearer or jwt, got %q", provider.Type)

		return nil
	}

	entry.finish()

	return provider
}

func parseUsers(entry *object) []BasicUser {
	items := requiredChildren(entry, "users")
	users := make([]BasicUser, 0, len(items))

	for _, item := range items {
		item.forbid("as plaintext; set passwordHash to a bcrypt hash, as printed by mcpcd config hash-password", "password")

		user := BasicUser{Username: item.requiredStr("username"), PasswordHash: item.requiredStr("passwordHash"), Roles: roles(item)}
		if user.PasswordHash != "" {
			checkPasswordHash(item, user.PasswordHash)
		}

		item.finish()

		users = append(users, user)
	}

	return users
}

// checkPasswordHash reports a passwordHash that is not a complete bcrypt hash of at least the minimum cost.
func checkPasswordHash(item *object, hash string) {
	cost, err := bcrypt.Cost([]byte(hash))

	switch {
	case err != nil || len(hash) != bcryptHashLength:
		item.report.add(joinKey(item.path, "passwordHash"), "must be a bcrypt hash, as printed by mcpcd config hash-password")
	case cost < MinBcryptCost:
		item.report.add(joinKey(item.path, "passwordHash"), "bcrypt cost %d is below the minimum of %d", cost, MinBcryptCost)
	}
}

func parseTokens(entry *object) []BearerToken {
	items := requiredChildren(entry, "tokens")
	tokens := make([]BearerToken, 0, len(items))

	for _, item := range items {
		token := BearerToken{Subject: item.str("subject"), Token: item.requiredStr("token"), Roles: roles(item)}
		if token.Token != "" && len(token.Token) < minTokenLength {
			item.report.add(joinKey(item.path, "token"), "must be at least %d characters; generate one with: openssl rand -hex 32", minTokenLength)
		}

		item.finish()

		tokens = append(tokens, token)
	}

	return tokens
}

func parseJWT(entry *object) *JWTProvider {
	provider := &JWTProvider{
		Issuer:       entry.requiredStr("issuer"),
		Audiences:    entry.strs("audiences"),
		SubjectClaim: entry.str("subjectClaim"),
	}

	if provider.Issuer != "" && !isHTTPURL(provider.Issuer) {
		entry.report.add(joinKey(entry.path, "issuer"), "must be an absolute http or https URL")
	}

	if len(provider.Audiences) == 0 {
		entry.report.add(joinKey(entry.path, "audiences"), "must list at least one audience")
	}

	if provider.SubjectClaim == "" {
		provider.SubjectClaim = defaultSubject
	}

	if jwks := entry.child("jwks"); jwks != nil {
		provider.JWKS = JWKS{URL: jwks.str("url"), CAFile: jwks.str("caFile"), BearerTokenFile: jwks.str("bearerTokenFile")}
		if provider.JWKS.URL != "" && !isHTTPURL(provider.JWKS.URL) {
			jwks.report.add(joinKey(jwks.path, "url"), "must be an absolute http or https URL")
		}

		jwks.finish()
	}

	for _, item := range requiredChildren(entry, "grants") {
		grant := Grant{Match: item.strMap("match"), Roles: roles(item)}
		if len(grant.Match) == 0 {
			item.report.add(joinKey(item.path, "match"), "must name at least one claim")
		}

		for claim, pattern := range grant.Match {
			if _, err := glob.Compile(pattern); err != nil {
				item.report.add(joinKey(joinKey(item.path, "match"), claim), "invalid glob %q: %v", pattern, err)
			}
		}

		item.finish()

		provider.Grants = append(provider.Grants, grant)
	}

	return provider
}

func requiredChildren(entry *object, key string) []*object {
	if !entry.has(key) {
		entry.report.add(entry.path, "missing required field %q", key)

		return nil
	}

	items := entry.children(key)
	if len(items) == 0 {
		entry.report.add(joinKey(entry.path, key), "must not be empty")
	}

	return items
}

func roles(entry *object) []Role {
	names := entry.strs("roles")
	if !entry.has("roles") {
		entry.report.add(entry.path, "missing required field %q", "roles")

		return nil
	}

	if len(names) == 0 {
		entry.report.add(joinKey(entry.path, "roles"), "must list user, admin or both")
	}

	result := make([]Role, 0, len(names))

	for index, name := range names {
		role := Role(name)
		if role != RoleUser && role != RoleAdmin {
			entry.report.add(joinIndex(joinKey(entry.path, "roles"), index), "must be user or admin, got %q", name)

			continue
		}

		if slices.Contains(result, role) {
			continue
		}

		result = append(result, role)
	}

	return result
}

func globs(entry *object, key string) []string {
	patterns := entry.strs(key)
	for index, pattern := range patterns {
		if _, err := glob.Compile(pattern); err != nil {
			entry.report.add(joinIndex(joinKey(entry.path, key), index), "invalid glob %q: %v", pattern, err)
		}
	}

	return patterns
}

func isHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)

	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
