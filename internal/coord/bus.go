// SPDX-License-Identifier: Elastic-2.0

package coord

import "sync"

// Bus is a tiny in-process fan-out signal. Coordination actions Publish to it;
// SSE subscribers wake and push a fresh snapshot — so the UI reflects an action
// the instant it happens, not on the next poll tick. Signals are coalesced
// (buffered-1, non-blocking) so a burst of actions never blocks a writer.
type Bus struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan struct{}]struct{}{}} }

// Subscribe returns a signal channel and an unsubscribe func.
func (b *Bus) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish wakes every subscriber (non-blocking, coalescing). Safe on a nil Bus.
func (b *Bus) Publish() {
	if b == nil {
		return
	}
	b.mu.Lock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
}
