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

package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestPutGetDeletePersist(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	credential := &Credential{
		Server: "github", ServerURL: "https://api.example/mcp", ClientID: "client", TokenURL: "https://auth.example/token",
		Scopes: []string{"repo"}, Token: &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)},
	}
	if err = s.Put(credential); err != nil {
		t.Fatal(err)
	}

	credential.Token.AccessToken = "mutated"

	got, ok := s.Get("github")
	if !ok || got.Token.AccessToken != "a" || got.UpdatedAt.IsZero() {
		t.Fatalf("get = %+v", got)
	}

	got.Scopes[0] = "mutated"
	if again, _ := s.Get("github"); again.Scopes[0] != "repo" {
		t.Error("Get must return a copy")
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reopened.Close() }()

	if got, ok = reopened.Get("github"); !ok || got.Token.RefreshToken != "r" {
		t.Fatalf("after reopen = %+v", got)
	}

	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(dir, fileName))
		if statErr != nil {
			t.Fatalf("stat: %v", statErr)
		}

		if info.Mode().Perm() != fileMode {
			t.Errorf("mode = %v, want %v", info.Mode().Perm(), fileMode)
		}
	}

	deleted, err := reopened.Delete("github")
	if err != nil || !deleted {
		t.Fatalf("delete = %t, %v", deleted, err)
	}

	if deleted, _ = reopened.Delete("github"); deleted {
		t.Error("second delete reported a credential")
	}
}

func TestSecondOpenIsLocked(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = first.Close() }()

	if _, err = Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open = %v, want ErrLocked", err)
	}
}

func TestCorruptFileFailsLoudly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt credentials file was accepted")
	}

	// The failed open must release the lock.
	if _, err := Open(dir); errors.Is(err, ErrLocked) {
		t.Fatal("lock leaked after a failed open")
	}
}
