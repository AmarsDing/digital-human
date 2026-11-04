package server

import (
	"encoding/hex"
	"sync"
	"time"
)

// VoiceTicket 与 events ticket 分库、分用途，绑定 session+client，不能互换。
type VoiceTicket struct {
	SessionID string
	ClientID  string
	Expires   time.Time
}

type VoiceTicketStore interface {
	Issue(sessionID, clientID string, ttl time.Duration) (string, time.Time, error)
	Peek(ticket string) (VoiceTicket, bool)
	Consume(ticket string) (VoiceTicket, bool)
}

type MemoryVoiceTicketStore struct {
	mu   sync.Mutex
	data map[string]VoiceTicket
}

func NewMemoryVoiceTicketStore() *MemoryVoiceTicketStore {
	return &MemoryVoiceTicketStore{data: map[string]VoiceTicket{}}
}

func (s *MemoryVoiceTicketStore) Issue(sessionID, clientID string, ttl time.Duration) (string, time.Time, error) {
	buf := make([]byte, 32)
	if err := readRandom(buf); err != nil {
		return "", time.Time{}, err
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	ticket := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, value := range s.data {
		if now.After(value.Expires) {
			delete(s.data, key)
		}
	}
	expires := now.Add(ttl)
	s.data[ticket] = VoiceTicket{SessionID: sessionID, ClientID: clientID, Expires: expires}
	return ticket, expires, nil
}

func (s *MemoryVoiceTicketStore) Peek(ticket string) (VoiceTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[ticket]
	if !ok || time.Now().After(value.Expires) {
		delete(s.data, ticket)
		return VoiceTicket{}, false
	}
	return value, true
}

func (s *MemoryVoiceTicketStore) Consume(ticket string) (VoiceTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[ticket]
	if !ok {
		return VoiceTicket{}, false
	}
	delete(s.data, ticket)
	if time.Now().After(value.Expires) {
		return VoiceTicket{}, false
	}
	return value, true
}
