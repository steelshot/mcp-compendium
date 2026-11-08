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

// Package agenteval measures how efficiently a scripted agent completes realistic tasks through the agent-facing tools:
// round trips, estimated tokens, search ranking and the size of the advertised tool list. Results are compared with a
// committed baseline so changes to tool descriptions, search or result shaping cannot silently make agents slower or
// more expensive. It contains only tests; run "go test ./internal/agenteval -v" to see the report and add -update to
// accept new results as the baseline.
package agenteval
