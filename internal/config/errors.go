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
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Error lists every problem found while loading the configuration, so one run reports them all.
type Error struct {
	Problems []Problem
}

// Problem is one invalid value, located by source file and path within it.
type Problem struct {
	Source  string
	Path    string
	Message string
}

// problems collects Problems for one source file.
type problems struct {
	source string
	list   *[]Problem
}

var (
	// ErrInvalid is matched by every *Error.
	ErrInvalid = errors.New("invalid configuration")

	errPlaceholder = errors.New("placeholder")
)

func (e *Error) Error() string {
	var builder strings.Builder

	builder.WriteString(ErrInvalid.Error())
	builder.WriteString(":")

	for _, problem := range e.Problems {
		builder.WriteString("\n  ")
		builder.WriteString(problem.String())
	}

	return builder.String()
}

// Unwrap makes errors.Is(err, ErrInvalid) hold.
func (*Error) Unwrap() error {
	return ErrInvalid
}

func (p Problem) String() string {
	location := p.Source
	if p.Path != "" {
		if location != "" {
			location += ": "
		}

		location += p.Path
	}

	if location == "" {
		return p.Message
	}

	return location + ": " + p.Message
}

func newProblems(source string, list *[]Problem) problems {
	return problems{source: source, list: list}
}

func asError(list []Problem) error {
	if len(list) == 0 {
		return nil
	}

	slices.SortStableFunc(list, func(a, b Problem) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Path, b.Path))
	})

	return &Error{Problems: list}
}

func (p problems) add(path, format string, args ...any) {
	*p.list = append(*p.list, Problem{Source: p.source, Path: path, Message: fmt.Sprintf(format, args...)})
}
