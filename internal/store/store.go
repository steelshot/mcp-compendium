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

// Package store persists OAuth credentials for downstream servers in the daemon's state directory. A lock file
// guarantees a single writer, so a second daemon or a local login against the same volume fails fast instead of
// racing token refreshes.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/oauth2"
)

// Credential is an OAuth client registration and token for one downstream server.
type Credential struct {
	Server string `json:"server"`
	// ServerURL binds the credential to the URL it was issued for; a configuration change invalidates it.
	ServerURL    string           `json:"serverUrl"`
	ClientID     string           `json:"clientId"`
	ClientSecret string           `json:"clientSecret,omitempty"`
	AuthURL      string           `json:"authUrl"`
	TokenURL     string           `json:"tokenUrl"`
	AuthStyle    oauth2.AuthStyle `json:"authStyle"`
	RedirectURL  string           `json:"redirectUrl"`
	Scopes       []string         `json:"scopes,omitempty"`
	// Dynamic marks clients registered by the daemon, which may be reused for the same redirect URL.
	Dynamic   bool          `json:"dynamic,omitempty"`
	Token     *oauth2.Token `json:"token"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// Store is the credential file in the state directory.
type Store struct {
	path string
	lock *flock.Flock

	mu          sync.Mutex
	credentials map[string]*Credential
}

// document is the on-disk format.
type document struct {
	Version     int                    `json:"version"`
	Credentials map[string]*Credential `json:"credentials"`
}

const (
	fileName      = "credentials.json"
	lockName      = ".lock"
	formatVersion = 1
	dirMode       = 0o700
	fileMode      = 0o600
)

// ErrLocked reports that another process holds the state directory.
var ErrLocked = errors.New("state directory is in use by another mcpcd process")

// Open creates the state directory if needed, takes its lock and loads the credentials.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	lock := flock.New(filepath.Join(dir, lockName))

	locked, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()

		return nil, fmt.Errorf("lock state directory: %w", err)
	}

	if !locked {
		_ = lock.Close()

		return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
	}

	s := &Store{path: filepath.Join(dir, fileName), lock: lock, credentials: make(map[string]*Credential)}
	if err = s.load(); err != nil {
		_ = lock.Close()

		return nil, err
	}

	return s, nil
}

// Get returns a copy of the credential for server.
func (s *Store) Get(server string) (*Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	credential, ok := s.credentials[server]
	if !ok {
		return nil, false
	}

	return credential.clone(), true
}

// Put stores credential and writes the file atomically.
func (s *Store) Put(credential *Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := credential.clone()
	stored.UpdatedAt = time.Now().UTC()

	next := maps.Clone(s.credentials)
	next[credential.Server] = stored

	if err := s.write(next); err != nil {
		return err
	}

	s.credentials = next

	return nil
}

// Delete removes the credential for server, reporting whether one existed.
func (s *Store) Delete(server string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.credentials[server]; !ok {
		return false, nil
	}

	next := maps.Clone(s.credentials)
	delete(next, server)

	if err := s.write(next); err != nil {
		return false, err
	}

	s.credentials = next

	return true, nil
}

// Close releases the lock.
func (s *Store) Close() error {
	if err := s.lock.Close(); err != nil {
		return fmt.Errorf("unlock state directory: %w", err)
	}

	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}

	var stored document
	if err = json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}

	if stored.Credentials != nil {
		s.credentials = stored.Credentials
	}

	return nil
}

// write replaces the file atomically: a crash leaves either the old or the new file, never a partial one.
func (s *Store) write(credentials map[string]*Credential) error {
	data, err := json.MarshalIndent(document{Version: formatVersion, Credentials: credentials}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(s.path), fileName+".*")
	if err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	defer func() { _ = os.Remove(temp.Name()) }()

	if err = temp.Chmod(fileMode); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = temp.Close()

		return fmt.Errorf("write credentials: %w", err)
	}

	if _, err = temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()

		return fmt.Errorf("write credentials: %w", err)
	}

	if err = temp.Sync(); err != nil {
		_ = temp.Close()

		return fmt.Errorf("write credentials: %w", err)
	}

	if err = temp.Close(); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	if err = os.Rename(temp.Name(), s.path); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	return nil
}

func (c *Credential) clone() *Credential {
	copied := *c
	copied.Scopes = append([]string(nil), c.Scopes...)

	if c.Token != nil {
		token := *c.Token
		copied.Token = &token
	}

	return &copied
}
