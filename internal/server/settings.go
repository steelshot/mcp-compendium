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

package server

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/kv"
)

// Settings configures the HTTP listener.
type Settings struct {
	Listen          string
	ShutdownTimeout time.Duration
	// PublicURL is the browser-reachable base URL of the daemon, or nil when browser-based logins are disabled.
	PublicURL *url.URL
}

const (
	StrListen = "listen"
	EnvListen = app.EnvPrefix + "LISTEN"
	DefListen = "127.0.0.1:8080"

	StrShutdownTimeout = "shutdown-timeout"
	EnvShutdownTimeout = app.EnvPrefix + "SHUTDOWN_TIMEOUT"
	DefShutdownTimeout = 15 * time.Second

	StrPublicURL = "public-url"
	EnvPublicURL = app.EnvPrefix + "PUBLIC_URL"
	DefPublicURL = ""
)

// Register defines the server flags.
func Register(flags *pflag.FlagSet) {
	flags.String(StrListen, DefListen, "address to listen on ("+EnvListen+")")
	flags.Duration(StrShutdownTimeout, DefShutdownTimeout, "grace period for in-flight requests on shutdown ("+EnvShutdownTimeout+")")
	flags.String(StrPublicURL, DefPublicURL, "browser-reachable base URL, enables login links sent to agents ("+EnvPublicURL+")")
}

// RegisterListen defines only the listen flag, for commands that probe a running daemon.
func RegisterListen(flags *pflag.FlagSet) {
	flags.String(StrListen, DefListen, "address the daemon listens on ("+EnvListen+")")
}

// Defaults maps each server key to its default value.
func Defaults() map[string]any {
	return map[string]any{StrListen: DefListen, StrShutdownTimeout: DefShutdownTimeout, StrPublicURL: DefPublicURL}
}

// EnvKeys maps each server environment variable to its key.
func EnvKeys() map[string]string {
	return map[string]string{EnvListen: StrListen, EnvShutdownTimeout: StrShutdownTimeout, EnvPublicURL: StrPublicURL}
}

// Load reads and validates the server settings.
func Load(k *koanf.Koanf) (Settings, error) {
	listen := kv.String(k, StrListen)
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return Settings{}, kv.Invalid(StrListen, EnvListen, listen, "host:port, for example 127.0.0.1:8080 or :8080")
	}

	shutdown, err := kv.Duration(k, StrShutdownTimeout, EnvShutdownTimeout)
	if err != nil {
		return Settings{}, err
	}

	settings := Settings{Listen: listen, ShutdownTimeout: shutdown}

	raw := kv.String(k, StrPublicURL)
	if raw == "" {
		return settings, nil
	}

	public, err := url.Parse(raw)
	if err != nil || (public.Scheme != "http" && public.Scheme != "https") || public.Host == "" {
		return Settings{}, kv.Invalid(StrPublicURL, EnvPublicURL, raw, "an absolute http or https URL")
	}

	public.Path = strings.TrimSuffix(public.Path, "/")
	settings.PublicURL = public

	return settings, nil
}

// LocalURL returns the base URL a process on the same host uses to reach the listener, mapping wildcard hosts to
// loopback.
func LocalURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse listen address %q: %w", listen, err)
	}

	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}

	return "http://" + net.JoinHostPort(host, port), nil
}

// IsLoopback reports whether the listen address only accepts connections from the local host.
func IsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}

	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}
