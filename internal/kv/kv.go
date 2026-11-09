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

// Package kv provides strict typed getters over a koanf instance. Values arrive as typed defaults, environment
// strings or flag values, so each getter accepts all three forms and reports malformed input with the flag and
// environment variable names the user actually typed.
package kv

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"
)

// ErrInvalid reports a setting whose value cannot be parsed into the expected type.
var ErrInvalid = errors.New("invalid setting")

// String returns the trimmed string value of key.
func String(k *koanf.Koanf, key string) string {
	return strings.TrimSpace(k.String(key))
}

// Duration returns key parsed as a Go duration such as "30s" or "1h30m".
func Duration(k *koanf.Koanf, key, env string) (time.Duration, error) {
	switch value := k.Get(key).(type) {
	case nil:
		return 0, nil
	case time.Duration:
		return value, nil
	case string:
		parsed, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return 0, Invalid(key, env, value, "a duration such as 30s or 1h")
		}

		return parsed, nil
	default:
		return 0, Invalid(key, env, fmt.Sprint(value), "a duration such as 30s or 1h")
	}
}

// Int returns key parsed as a base-10 integer.
func Int(k *koanf.Koanf, key, env string) (int, error) {
	switch value := k.Get(key).(type) {
	case nil:
		return 0, nil
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, Invalid(key, env, value, "an integer")
		}

		return parsed, nil
	default:
		return 0, Invalid(key, env, fmt.Sprint(value), "an integer")
	}
}

// OneOf returns the lower-cased string value of key, which must be one of allowed.
func OneOf(k *koanf.Koanf, key, env string, allowed ...string) (string, error) {
	value := strings.ToLower(String(k, key))
	if slices.Contains(allowed, value) {
		return value, nil
	}

	return "", Invalid(key, env, value, "one of "+strings.Join(allowed, ", "))
}

// Invalid builds an ErrInvalid error naming both the flag and the environment variable.
func Invalid(key, env, value, expected string) error {
	return fmt.Errorf("%w: --%s / %s is %q, expected %s", ErrInvalid, key, env, value, expected)
}
