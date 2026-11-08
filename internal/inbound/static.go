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

package inbound

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/steelshot/mcp-compendium/internal/config"
)

// basicProvider accepts HTTP Basic credentials checked against bcrypt hashes.
type basicProvider struct {
	name     string
	users    []config.BasicUser
	decoy    []byte
	verified *verifiedCredentials
}

// bearerProvider accepts static bearer tokens.
type bearerProvider struct {
	name   string
	tokens []config.BearerToken
}

// verifiedCredentials remembers credentials that recently passed a bcrypt check. bcrypt is deliberately slow and
// clients send credentials with every request, so repeating the check each time would make every call slow. Entries are
// keyed by an HMAC under a key that exists only in this process, so neither passwords nor anything that could be
// guessed against offline are kept.
type verifiedCredentials struct {
	mu      sync.Mutex
	key     []byte
	entries map[[sha256.Size]byte]time.Time
	now     func() time.Time
}

const (
	verifiedLifetime   = 5 * time.Minute
	maxVerifiedEntries = 1024
)

func newBasicProvider(provider *config.Provider) (*basicProvider, error) {
	cost := config.MinBcryptCost
	for _, user := range provider.Users {
		if userCost, err := bcrypt.Cost([]byte(user.PasswordHash)); err == nil && userCost > cost {
			cost = userCost
		}
	}

	// The decoy is checked for unknown usernames so that they take as long to reject as wrong passwords.
	decoy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), cost)
	if err != nil {
		return nil, fmt.Errorf("prepare basic provider %s: %w", provider.Name, err)
	}

	return &basicProvider{name: provider.Name, users: provider.Users, decoy: decoy, verified: newVerifiedCredentials(time.Now)}, nil
}

func newBearerProvider(provider *config.Provider) *bearerProvider {
	return &bearerProvider{name: provider.Name, tokens: provider.Tokens}
}

func newVerifiedCredentials(now func() time.Time) *verifiedCredentials {
	return &verifiedCredentials{key: []byte(rand.Text()), entries: make(map[[sha256.Size]byte]time.Time), now: now}
}

// authenticate checks every username, and runs one bcrypt comparison whether or not the username exists, so the time
// taken does not reveal which usernames exist.
func (p *basicProvider) authenticate(username, password string) *Principal {
	var matched *config.BasicUser

	for index := range p.users {
		user := &p.users[index]
		if digestEqual(user.Username, username) && matched == nil {
			matched = user
		}
	}

	if matched == nil {
		_ = bcrypt.CompareHashAndPassword(p.decoy, []byte(password))

		return nil
	}

	digest := p.verified.digest(username, password)
	if !p.verified.contains(digest) {
		if bcrypt.CompareHashAndPassword([]byte(matched.PasswordHash), []byte(password)) != nil {
			return nil
		}

		p.verified.add(digest)
	}

	return &Principal{Provider: p.name, Subject: matched.Username, Roles: matched.Roles}
}

// authenticate checks every token so the time taken does not reveal which one matched.
func (p *bearerProvider) authenticate(token string) *Principal {
	var matched *config.BearerToken

	for index := range p.tokens {
		candidate := &p.tokens[index]
		if digestEqual(candidate.Token, token) && matched == nil {
			matched = candidate
		}
	}

	if matched == nil {
		return nil
	}

	subject := matched.Subject
	if subject == "" {
		subject = p.name
	}

	return &Principal{Provider: p.name, Subject: subject, Roles: matched.Roles}
}

func (v *verifiedCredentials) digest(username, password string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, v.key)
	_, _ = mac.Write([]byte(username))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(password))

	var digest [sha256.Size]byte

	copy(digest[:], mac.Sum(nil))

	return digest
}

func (v *verifiedCredentials) contains(digest [sha256.Size]byte) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	expires, ok := v.entries[digest]
	if ok && v.now().Before(expires) {
		return true
	}

	delete(v.entries, digest)

	return false
}

func (v *verifiedCredentials) add(digest [sha256.Size]byte) {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := v.now()
	if len(v.entries) >= maxVerifiedEntries {
		for key, expires := range v.entries {
			if !now.Before(expires) {
				delete(v.entries, key)
			}
		}

		if len(v.entries) >= maxVerifiedEntries {
			clear(v.entries)
		}
	}

	v.entries[digest] = now.Add(verifiedLifetime)
}
