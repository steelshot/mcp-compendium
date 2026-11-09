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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"go.yaml.in/yaml/v3"
)

const maxFileBytes = 1 << 20

var (
	errDuplicateKey = errors.New("duplicate key")
	errFileTooLarge = errors.New("file too large")
	errNotObject    = errors.New("top level must be an object")
	errTrailingData = errors.New("trailing data after the top-level value")
	errUnsupported  = errors.New("unsupported value")
)

// supportedExtension reports whether name has a configuration file extension.
func supportedExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".jsonc", ".yaml", ".yml", ".toml":
		return true
	default:
		return false
	}
}

// decode parses data according to the extension of name into a tree of map[string]any, []any, string, bool, int64
// and float64 values.
func decode(name string, data []byte) (map[string]any, error) {
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", errFileTooLarge, len(data), maxFileBytes)
	}

	var (
		tree any
		err  error
	)

	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".jsonc":
		tree, err = decodeJSON(data)
	case ".yaml", ".yml":
		tree, err = decodeYAML(data)
	case ".toml":
		tree, err = decodeTOML(data)
	default:
		return nil, fmt.Errorf("%w: file extension %q", errUnsupported, filepath.Ext(name))
	}

	if err != nil {
		return nil, err
	}

	if tree == nil {
		return map[string]any{}, nil
	}

	object, ok := tree.(map[string]any)
	if !ok {
		return nil, errNotObject
	}

	return object, nil
}

// decodeJSON accepts JSON with comments and trailing commas, and rejects duplicate keys, which encoding/json would
// silently resolve to the last value.
func decodeJSON(data []byte) (any, error) {
	standard, err := hujson.Standardize(data)
	if err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(standard))
	decoder.UseNumber()

	value, err := readJSONValue(decoder)
	if err != nil {
		return nil, err
	}

	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse JSON: %w", errTrailingData)
	}

	return value, nil
}

func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	switch value := token.(type) {
	case json.Delim:
		if value == '{' {
			return readJSONObject(decoder)
		}

		return readJSONArray(decoder)
	case json.Number:
		return normalizeNumber(value)
	default:
		return value, nil
	}
}

func readJSONObject(decoder *json.Decoder) (any, error) {
	object := make(map[string]any)

	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("parse JSON: %w", err)
		}

		key, _ := token.(string)
		if _, exists := object[key]; exists {
			return nil, fmt.Errorf("parse JSON: %w %q", errDuplicateKey, key)
		}

		if object[key], err = readJSONValue(decoder); err != nil {
			return nil, err
		}
	}

	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	return object, nil
}

func readJSONArray(decoder *json.Decoder) (any, error) {
	array := make([]any, 0)

	for decoder.More() {
		value, err := readJSONValue(decoder)
		if err != nil {
			return nil, err
		}

		array = append(array, value)
	}

	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	return array, nil
}

func normalizeNumber(number json.Number) (any, error) {
	if integer, err := number.Int64(); err == nil {
		return integer, nil
	}

	float, err := number.Float64()
	if err != nil {
		return nil, fmt.Errorf("parse JSON: number %s: %w", number, err)
	}

	return float, nil
}

// decodeYAML relies on yaml.v3, which already rejects duplicate mapping keys.
func decodeYAML(data []byte) (any, error) {
	var tree any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	return normalize(tree)
}

// decodeTOML relies on go-toml, which already rejects redefined keys and tables.
func decodeTOML(data []byte) (any, error) {
	var tree map[string]any
	if err := toml.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("parse TOML: %w", err)
	}

	return normalize(tree)
}

// normalize converts decoder-specific representations into the shared tree types.
func normalize(value any) (any, error) {
	switch typed := value.(type) {
	case nil, string, bool, int64, float64:
		return typed, nil
	case int:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return nil, fmt.Errorf("%w: integer %d out of range", errUnsupported, typed)
		}

		return int64(typed), nil
	case map[string]any:
		for key, item := range typed {
			normalized, err := normalize(item)
			if err != nil {
				return nil, err
			}

			typed[key] = normalized
		}

		return typed, nil
	case []any:
		for index, item := range typed {
			normalized, err := normalize(item)
			if err != nil {
				return nil, err
			}

			typed[index] = normalized
		}

		return typed, nil
	default:
		return nil, fmt.Errorf("%w: %T; use strings, numbers, booleans, lists and maps with string keys", errUnsupported, value)
	}
}
