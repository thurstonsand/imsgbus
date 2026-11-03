package matrix

import (
	"context"
	"sync"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
)

// cursor saves how far a login has bridged. Portals handle their events concurrently, so it holds
// just before the oldest row still in flight: a restart replays from there, and bridgev2 drops
// the replayed messages it already has.
type cursor struct {
	login *bridgev2.UserLogin
	meta  *UserLoginMetadata

	lock     sync.Mutex
	inFlight map[int64]struct{}
	newest   int64
}

func newCursor(login *bridgev2.UserLogin, meta *UserLoginMetadata) *cursor {
	return &cursor{login: login, meta: meta, inFlight: make(map[int64]struct{}), newest: meta.Cursor}
}

func (c *cursor) position() int64 {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.meta.Cursor
}

func (c *cursor) start(row int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.inFlight[row] = struct{}{}
}

func (c *cursor) finish(ctx context.Context, row int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.inFlight, row)
	c.newest = max(c.newest, row)
	next := c.newest
	for pending := range c.inFlight {
		next = min(next, pending-1)
	}
	if next <= c.meta.Cursor {
		return
	}
	c.meta.Cursor = next
	if err := c.login.Save(ctx); err != nil {
		zerolog.Ctx(ctx).Err(err).Int64("cursor", next).Msg("Failed to save cursor")
	}
}
