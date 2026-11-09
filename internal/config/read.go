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
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// object reads typed fields from one decoded map, recording a problem for every missing, mistyped or unknown key.
type object struct {
	path   string
	values map[string]any
	seen   map[string]struct{}
	report problems
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_-]*$`)

func newObject(path string, values map[string]any, report problems) *object {
	return &object{path: path, values: values, seen: make(map[string]struct{}, len(values)), report: report}
}

func isString(value any) bool {
	_, ok := value.(string)

	return ok
}

func describe(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return strconv.Quote(typed)
	case bool, int64, float64:
		return fmt.Sprint(typed)
	case map[string]any:
		return "a map"
	case []any:
		return "a list"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func joinKey(path, key string) string {
	if !identifierPattern.MatchString(key) {
		return path + "[" + strconv.Quote(key) + "]"
	}

	if path == "" {
		return key
	}

	return path + "." + key
}

func joinIndex(path string, index int) string {
	return path + "[" + strconv.Itoa(index) + "]"
}

// has reports whether key is present.
func (o *object) has(key string) bool {
	_, ok := o.values[key]

	return ok
}

// str returns the string at key, or "" when absent.
func (o *object) str(key string) string {
	value, ok := o.take(key)
	if !ok {
		return ""
	}

	text, ok := value.(string)
	if !ok {
		o.mistyped(key, "a string", value)
	}

	return text
}

// requiredStr returns the non-empty string at key, reporting it when absent or empty.
func (o *object) requiredStr(key string) string {
	if !o.has(key) {
		o.report.add(o.path, "missing required field %q", key)

		return ""
	}

	text := o.str(key)
	if text == "" && isString(o.values[key]) {
		o.report.add(joinKey(o.path, key), "must not be empty")
	}

	return text
}

// boolean returns the boolean at key, or fallback when absent.
func (o *object) boolean(key string, fallback bool) bool {
	value, ok := o.take(key)
	if !ok {
		return fallback
	}

	flag, ok := value.(bool)
	if !ok {
		o.mistyped(key, "true or false", value)

		return fallback
	}

	return flag
}

// integer returns the integer at key, or 0 when absent.
func (o *object) integer(key string) int {
	value, ok := o.take(key)
	if !ok {
		return 0
	}

	number, ok := value.(int64)
	if !ok {
		o.mistyped(key, "an integer", value)

		return 0
	}

	return int(number)
}

// duration returns the Go duration string at key parsed, or 0 when absent.
func (o *object) duration(key string) time.Duration {
	text := o.str(key)
	if text == "" {
		return 0
	}

	parsed, err := time.ParseDuration(text)
	if err != nil || parsed <= 0 {
		o.report.add(joinKey(o.path, key), "%q is not a positive duration such as 30s, 5m or 1h30m", text)

		return 0
	}

	return parsed
}

// strs returns the list of non-empty strings at key, or nil when absent.
func (o *object) strs(key string) []string {
	items := o.list(key)
	if items == nil {
		return nil
	}

	result := make([]string, 0, len(items))

	for index, item := range items {
		text, ok := item.(string)
		if !ok || text == "" {
			o.report.add(joinIndex(joinKey(o.path, key), index), "must be a non-empty string, got %s", describe(item))

			continue
		}

		result = append(result, text)
	}

	return result
}

// strMap returns the string-to-string map at key, or nil when absent.
func (o *object) strMap(key string) map[string]string {
	value, ok := o.take(key)
	if !ok {
		return nil
	}

	values, ok := value.(map[string]any)
	if !ok {
		o.mistyped(key, "a map of strings", value)

		return nil
	}

	result := make(map[string]string, len(values))

	for name, item := range values {
		text, ok := item.(string)
		if !ok {
			o.report.add(joinKey(joinKey(o.path, key), name), "must be a string, got %s", describe(item))

			continue
		}

		result[name] = text
	}

	return result
}

// child returns the nested object at key, or nil when absent or mistyped.
func (o *object) child(key string) *object {
	value, ok := o.take(key)
	if !ok {
		return nil
	}

	values, ok := value.(map[string]any)
	if !ok {
		o.mistyped(key, "a map", value)

		return nil
	}

	return newObject(joinKey(o.path, key), values, o.report)
}

// children returns the list of objects at key, or nil when absent.
func (o *object) children(key string) []*object {
	items := o.list(key)
	if items == nil {
		return nil
	}

	result := make([]*object, 0, len(items))

	for index, item := range items {
		values, ok := item.(map[string]any)
		if !ok {
			o.report.add(joinIndex(joinKey(o.path, key), index), "must be a map, got %s", describe(item))

			continue
		}

		result = append(result, newObject(joinIndex(joinKey(o.path, key), index), values, o.report))
	}

	return result
}

// forbid reports each present key as invalid for the given reason and marks it read.
func (o *object) forbid(reason string, keys ...string) {
	for _, key := range keys {
		if _, ok := o.take(key); ok {
			o.report.add(joinKey(o.path, key), "not allowed %s", reason)
		}
	}
}

// ignore marks keys as read without validating them.
func (o *object) ignore(keys ...string) {
	for _, key := range keys {
		o.seen[key] = struct{}{}
	}
}

// finish reports every key that was never read.
func (o *object) finish() {
	unknown := make([]string, 0)

	for key := range o.values {
		if _, ok := o.seen[key]; !ok {
			unknown = append(unknown, key)
		}
	}

	slices.Sort(unknown)

	for _, key := range unknown {
		o.report.add(joinKey(o.path, key), "unknown field")
	}
}

func (o *object) list(key string) []any {
	value, ok := o.take(key)
	if !ok {
		return nil
	}

	items, ok := value.([]any)
	if !ok {
		o.mistyped(key, "a list", value)

		return nil
	}

	return items
}

func (o *object) take(key string) (any, bool) {
	o.seen[key] = struct{}{}
	value, ok := o.values[key]

	return value, ok && value != nil
}

func (o *object) mistyped(key, expected string, value any) {
	o.report.add(joinKey(o.path, key), "must be %s, got %s", expected, describe(value))
}
