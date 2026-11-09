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

package client

import (
	"net/url"
	"strings"

	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/kv"
)

// Settings locates the daemon and the credentials to present.
type Settings struct {
	URL       *url.URL
	Token     string
	TokenFile string
	Username  string
	Password  string
}

const (
	StrURL = "url"
	EnvURL = app.EnvPrefix + "URL"
	DefURL = "http://127.0.0.1:8080"

	StrToken = "token"
	EnvToken = app.EnvPrefix + "TOKEN"
	DefToken = ""

	StrTokenFile = "token-file"
	EnvTokenFile = app.EnvPrefix + "TOKEN_FILE"
	DefTokenFile = ""

	StrUsername = "username"
	EnvUsername = app.EnvPrefix + "USERNAME"
	DefUsername = ""

	StrPassword = "password"
	EnvPassword = app.EnvPrefix + "PASSWORD"
	DefPassword = ""
)

// Register defines the client flags.
func Register(flags *pflag.FlagSet) {
	flags.String(StrURL, DefURL, "daemon base URL ("+EnvURL+")")
	flags.String(StrToken, DefToken, "bearer token for the admin API; prefer the environment variable ("+EnvToken+")")
	flags.String(StrTokenFile, DefTokenFile, "file containing the bearer token, such as a projected service account token ("+EnvTokenFile+")")
	flags.String(StrUsername, DefUsername, "basic auth username ("+EnvUsername+")")
	flags.String(StrPassword, DefPassword, "basic auth password; prefer the environment variable ("+EnvPassword+")")
}

// Defaults maps each client key to its default value.
func Defaults() map[string]any {
	return map[string]any{StrURL: DefURL, StrToken: DefToken, StrTokenFile: DefTokenFile, StrUsername: DefUsername, StrPassword: DefPassword}
}

// EnvKeys maps each client environment variable to its key.
func EnvKeys() map[string]string {
	return map[string]string{EnvURL: StrURL, EnvToken: StrToken, EnvTokenFile: StrTokenFile, EnvUsername: StrUsername, EnvPassword: StrPassword}
}

// Load reads and validates the client settings.
func Load(k *koanf.Koanf) (Settings, error) {
	raw := strings.TrimSuffix(kv.String(k, StrURL), "/")

	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Settings{}, kv.Invalid(StrURL, EnvURL, raw, "an absolute http or https URL")
	}

	settings := Settings{
		URL: parsed, Token: kv.String(k, StrToken), TokenFile: kv.String(k, StrTokenFile),
		Username: kv.String(k, StrUsername), Password: k.String(StrPassword),
	}

	if (settings.Token != "" || settings.TokenFile != "") && settings.Username != "" {
		return Settings{}, errAmbiguousCredentials
	}

	return settings, nil
}
