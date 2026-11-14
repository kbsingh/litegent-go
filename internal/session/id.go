// Package session manages Litegent's persistent, human-readable sessions:
// unique IDs, per-session directories, manifests, context persistence,
// and resume validation.
package session

import (
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	minIDWords  = 3
	maxIDWords  = 4
	maxIDLength = 64
)

var idPattern = regexp.MustCompile(`^[a-z]+(?:-[a-z]+){2,3}$`)

// reservedIDs are top-level names under top_dir that must never be used as
// session directory names.
var reservedIDs = map[string]struct{}{
	"config.json": {},
	"system.md":   {},
	"skills":      {},
	"logs":        {},
	"spool":       {},
	"tmp":         {},
	"sessions":    {},
}

// ValidateID checks the user-supplied resume identifier without touching
// the filesystem. It rejects separators, traversal, uppercase, and any
// shape other than 3 or 4 lowercase hyphenated words.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("session id must not be empty")
	}
	if len(id) > maxIDLength {
		return fmt.Errorf("session id must be at most %d characters", maxIDLength)
	}
	if _, ok := reservedIDs[id]; ok {
		return fmt.Errorf("session id %q is reserved", id)
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("session id %q must be 3 or 4 lowercase words joined by hyphens", id)
	}
	return nil
}

// Generator produces random session IDs. Tests can construct one with a
// deterministic rand.Rand.
type Generator struct {
	Rand *rand.Rand
}

// NewGenerator returns a generator seeded from the current time and PID.
func NewGenerator() *Generator {
	return &Generator{
		Rand: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(os.Getpid()))),
	}
}

// ID returns a random 3- or 4-word ID. It does not check uniqueness.
func (g *Generator) ID() string {
	n := minIDWords
	if g.Rand.IntN(2) == 1 {
		n = maxIDWords
	}
	words := []string{
		adjectives[g.Rand.IntN(len(adjectives))],
		nouns[g.Rand.IntN(len(nouns))],
		verbs[g.Rand.IntN(len(verbs))],
	}
	if n == maxIDWords {
		words = append(words, adverbs[g.Rand.IntN(len(adverbs))])
	}
	return strings.Join(words, "-")
}
