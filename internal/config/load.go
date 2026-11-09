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

// Package config loads the server and authentication configuration from a file or a conf.d-style directory in JSON,
// JSONC, YAML or TOML, resolves ${env:...} and ${file:...} placeholders, and reports every problem with its source and
// path. The published JSON Schema in the schema package describes the same format for editors.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// loader carries the injectable environment and file access used while loading.
type loader struct {
	expander expander
	readFile readFileFunc
}

var errNoFiles = errors.New("no configuration files")

// Read reads the configuration at path, which is either a file or a directory whose *.json, *.jsonc, *.yaml, *.yml and
// *.toml files are merged in lexical order. Hidden entries are skipped, which covers Kubernetes ConfigMap bookkeeping.
func Read(path string) (*Config, error) {
	return loader{expander: newExpander(nil, nil), readFile: os.ReadFile}.load(path)
}

func configFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s does not exist", errNoFiles, path)
		}

		return nil, fmt.Errorf("read configuration: %w", err)
	}

	if !info.IsDir() {
		if !supportedExtension(path) {
			return nil, fmt.Errorf("%w: %s must end in .json, .jsonc, .yaml, .yml or .toml", errNoFiles, path)
		}

		return []string{path}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration directory: %w", err)
	}

	files := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !supportedExtension(name) {
			continue
		}

		full := filepath.Join(path, name)

		// Stat follows symlinks, which Kubernetes uses for every projected ConfigMap and Secret key.
		target, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("read configuration: %w", err)
		}

		if target.Mode().IsRegular() {
			files = append(files, full)
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w in %s; add a *.yaml, *.json, *.jsonc or *.toml file", errNoFiles, path)
	}

	slices.Sort(files)

	return files, nil
}

// merge combines fragments; a server or provider defined in two files, or allowAnonymous set twice, is a problem rather
// than an override, so the outcome never depends on file order.
func merge(fragments []fragment, list *[]Problem) *Config {
	config := &Config{}
	serverSources := make(map[string]string)
	providerSources := make(map[string]string)
	anonymousSource := ""

	for _, part := range fragments {
		report := newProblems(part.source, list)

		for _, server := range part.servers {
			if first, exists := serverSources[server.Name]; exists {
				report.add(joinKey("servers", server.Name), "already defined in %s", first)

				continue
			}

			serverSources[server.Name] = part.source
			config.Servers = append(config.Servers, server)
		}

		for _, provider := range part.providers {
			if first, exists := providerSources[provider.Name]; exists && provider.Name != "" {
				report.add("auth.providers", "provider %q already defined in %s", provider.Name, first)

				continue
			}

			providerSources[provider.Name] = part.source
			config.Auth.Providers = append(config.Auth.Providers, provider)
		}

		if part.allowAnonymous != nil {
			if anonymousSource != "" {
				report.add("auth.allowAnonymous", "already set in %s", anonymousSource)

				continue
			}

			anonymousSource = part.source
			config.Auth.AllowAnonymous = *part.allowAnonymous
		}
	}

	slices.SortFunc(config.Servers, func(a, b *Server) int { return cmp.Compare(a.Name, b.Name) })

	return config
}

func (l loader) load(path string) (*Config, error) {
	files, err := configFiles(path)
	if err != nil {
		return nil, err
	}

	var list []Problem

	fragments := make([]fragment, 0, len(files))

	for _, file := range files {
		report := newProblems(file, &list)

		data, err := l.readFile(file)
		if err != nil {
			report.add("", "%v", err)

			continue
		}

		tree, err := decode(file, data)
		if err != nil {
			report.add("", "%v", err)

			continue
		}

		l.expander.expandTree(tree, "", report)
		fragments = append(fragments, parseFragment(file, tree, report))
	}

	config := merge(fragments, &list)
	config.Sources = files

	if err := asError(list); err != nil {
		return nil, err
	}

	return config, nil
}
