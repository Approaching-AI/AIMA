package agent

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

type session struct {
	messages []Message
	lastUsed time.Time
}

// SessionStore is an in-memory store for multi-turn agent conversations.
// Sessions expire after TTL and are capped at maxMsgs to prevent token overflow.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
	ttl      time.Duration
	maxMsgs  int
}

// NewSessionStore creates a SessionStore with 30min TTL and 50 message cap.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*session),
		ttl:      30 * time.Minute,
		maxMsgs:  50,
	}
}

// Get returns a copy of the session messages and refreshes TTL. Returns false if not found or expired.
func (s *SessionStore) Get(id string) ([]Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()

	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	sess.lastUsed = time.Now()
	msgs := make([]Message, len(sess.messages))
	copy(msgs, sess.messages)
	return msgs, true
}

// Save stores messages for a session, trimming to maxMsgs (keeping system prompt + newest messages).
func (s *SessionStore) Save(id string, messages []Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()

	if len(messages) > s.maxMsgs {
		// Keep system prompt (first message) + newest messages
		trimmed := make([]Message, 0, s.maxMsgs)
		trimmed = append(trimmed, messages[0])
		trimmed = append(trimmed, messages[len(messages)-s.maxMsgs+1:]...)
		messages = trimmed
	}

	stored := make([]Message, len(messages))
	copy(stored, messages)
	s.sessions[id] = &session{messages: stored, lastUsed: time.Now()}
}

func (s *SessionStore) cleanup() {
	now := time.Now()
	for id, sess := range s.sessions {
		if now.Sub(sess.lastUsed) > s.ttl {
			delete(s.sessions, id)
		}
	}
}

// GenerateID creates a random session ID.
func GenerateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
