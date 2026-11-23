package terminal

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

// maxTicketsPerUser bounds the tickets one user may hold at once. A ticket is
// issued per terminal tab and lives two minutes, so an honest user holds one
// or two; the cap keeps a loop from growing the store.
const maxTicketsPerUser = 16

// errTooManyTickets is returned by issue once a user holds the cap.
var errTooManyTickets = errors.New("too many outstanding terminal tickets")

// ticketStore holds the outstanding terminal tickets. A ticket is an opaque
// random string bound to the user it was issued to, valid until expiresAt,
// and deleted as it is redeemed, so it can be used exactly once. Expired
// tickets are dropped lazily from a FIFO ordered by expiry, which keeps
// redemption O(1) and needs no timer per ticket.
type ticketStore struct {
	mu     sync.Mutex
	now    func() time.Time
	byID   map[string]ticket
	byUser map[string]int
	// expiry is ordered by expiresAt: every issue appends the same TTL later.
	expiry []string
}

type ticket struct {
	userID    string
	expiresAt time.Time
}

func newTicketStore(now func() time.Time) *ticketStore {
	return &ticketStore{
		now:    now,
		byID:   map[string]ticket{},
		byUser: map[string]int{},
	}
}

// issue mints a ticket for userID that stays valid for ttl.
func (s *ticketStore) issue(userID string, ttl time.Duration) (string, error) {
	// 128 bits of crypto-random text: not guessable, safe in a URL.
	id := rand.Text()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpired()
	if s.byUser[userID] >= maxTicketsPerUser {
		return "", errTooManyTickets
	}
	s.byID[id] = ticket{userID: userID, expiresAt: s.now().Add(ttl)}
	s.byUser[userID]++
	s.expiry = append(s.expiry, id)
	return id, nil
}

// redeem reports the user a ticket was issued to and voids it.
func (s *ticketStore) redeem(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpired()
	tk, ok := s.byID[id]
	if !ok {
		return "", false
	}
	s.drop(id, tk)
	return tk.userID, true
}

// purgeExpired drops every ticket past its time. The caller holds s.mu.
func (s *ticketStore) purgeExpired() {
	now := s.now()
	for len(s.expiry) > 0 {
		id := s.expiry[0]
		tk, ok := s.byID[id]
		if ok && now.Before(tk.expiresAt) {
			break
		}
		s.expiry = s.expiry[1:]
		if ok {
			s.drop(id, tk)
		}
	}
}

// drop removes a ticket that was redeemed or expired. The caller holds s.mu.
func (s *ticketStore) drop(id string, tk ticket) {
	delete(s.byID, id)
	if s.byUser[tk.userID]--; s.byUser[tk.userID] <= 0 {
		delete(s.byUser, tk.userID)
	}
}
