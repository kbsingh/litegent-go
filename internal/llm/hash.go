package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// HashMessages returns a stable SHA-256 over the canonical JSON encoding
// of messages. It is used to correlate a logged request with the exact
// durable context blob saved for that step.
func HashMessages(messages []Message) (string, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("marshaling messages for hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
