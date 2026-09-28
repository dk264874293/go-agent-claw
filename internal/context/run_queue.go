package context

import "context"

// RunTicket 代表一个按登记顺序等待的会话任务。
type RunTicket struct {
	session  *Session
	ready    chan struct{}
	released bool // 由 Session.mu 保护
}

// ReserveRun 在启动异步工作前登记，以保持入口接收顺序。
func (s *Session) ReserveRun() *RunTicket {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket := &RunTicket{session: s, ready: make(chan struct{})}
	s.runQueue = append(s.runQueue, ticket)
	if len(s.runQueue) == 1 {
		close(ticket.ready)
	}
	return ticket
}

func (t *RunTicket) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		t.Release()
		return ctx.Err()
	case <-t.ready:
		if err := ctx.Err(); err != nil {
			t.Release()
			return err
		}
		return nil
	}
}

// Release 同时支持完成后的释放和等待中的取消，重复调用无副作用。
func (t *RunTicket) Release() {
	s := t.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.released {
		return
	}
	t.released = true
	for i, queued := range s.runQueue {
		if queued != t {
			continue
		}
		copy(s.runQueue[i:], s.runQueue[i+1:])
		s.runQueue[len(s.runQueue)-1] = nil
		s.runQueue = s.runQueue[:len(s.runQueue)-1]
		if i == 0 && len(s.runQueue) > 0 {
			close(s.runQueue[0].ready)
		}
		break
	}
}
