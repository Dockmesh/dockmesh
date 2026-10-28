package auth

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

const wsTicketTTL = 30 * time.Second

// ErrTicketUsed is returned when a WS ticket is presented a second time.
var ErrTicketUsed = errors.New("ws ticket already used")

// WSTicket names who a WebSocket connection runs as: a user session or an
// API token. Role and scope are not carried; the handler reads them fresh
// when the connection opens and while it stays open.
type WSTicket struct {
	UserID     string `json:"uid,omitempty"`
	SessionID  string `json:"sid,omitempty"`
	APITokenID int64  `json:"tok,omitempty"`
	Perm       string `json:"perm"`
	TypedClaims
}

// IssueWSTicket mints a single-use ticket valid for 30 seconds.
func (s *Service) IssueWSTicket(t WSTicket) (string, error) {
	t.ID = uuid.NewString()
	return s.keys.Sign(KindWSTicket, wsTicketTTL, &t)
}

// ConsumeWSTicket verifies a ticket and marks it used. A ticket is
// accepted once; replays fail with ErrTicketUsed.
func (s *Service) ConsumeWSTicket(token string) (*WSTicket, error) {
	t := &WSTicket{}
	if err := s.keys.Parse(KindWSTicket, token, t); err != nil {
		return nil, err
	}
	if t.ID == "" {
		return nil, ErrInvalidToken
	}
	if !s.tickets.consume(t.ID, t.ExpiresAt.Time) {
		return nil, ErrTicketUsed
	}
	return t, nil
}

// ticketStore remembers consumed ticket ids until the ticket would have
// expired anyway. In memory is enough: Dockmesh runs as a single server
// and a restart invalidates nothing that matters (tickets live 30 s).
type ticketStore struct {
	mu   sync.Mutex
	used map[string]time.Time
}

func newTicketStore() *ticketStore {
	return &ticketStore{used: map[string]time.Time{}}
}

func (ts *ticketStore) consume(id string, expires time.Time) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := time.Now()
	for k, exp := range ts.used {
		if now.After(exp) {
			delete(ts.used, k)
		}
	}
	if _, seen := ts.used[id]; seen {
		return false
	}
	ts.used[id] = expires
	return true
}
