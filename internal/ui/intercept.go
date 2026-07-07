// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"sync"

	"golang.org/x/net/websocket"
)

type InterceptSession struct {
	AgentConn    *websocket.Conn
	OperatorConn *websocket.Conn
	Done         chan struct{}
}

type InterceptRegistry struct {
	mu       sync.Mutex
	sessions map[string]*InterceptSession
}

var Registry = &InterceptRegistry{
	sessions: make(map[string]*InterceptSession),
}

func (r *InterceptRegistry) ServeWS(wsConn *websocket.Conn) {
	agent := wsConn.Request().URL.Query().Get("agent")
	role := wsConn.Request().URL.Query().Get("role")
	if agent == "" || role == "" {
		_ = wsConn.Close()
		return
	}

	r.mu.Lock()
	sess, ok := r.sessions[agent]
	if !ok {
		sess = &InterceptSession{Done: make(chan struct{})}
		r.sessions[agent] = sess
	}
	
	if role == "agent" {
		sess.AgentConn = wsConn
	} else if role == "operator" {
		sess.OperatorConn = wsConn
	}
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		if s, ok := r.sessions[agent]; ok {
			select {
			case <-s.Done:
			default:
				close(s.Done)
			}
			delete(r.sessions, agent)
		}
		r.mu.Unlock()
		_ = wsConn.Close()
	}()

	if role == "agent" {
		buf := make([]byte, 8192)
		for {
			n, err := wsConn.Read(buf)
			if err != nil {
				break
			}
			r.mu.Lock()
			op := sess.OperatorConn
			r.mu.Unlock()
			if op != nil {
				_, _ = op.Write(buf[:n])
			}
		}
	} else if role == "operator" {
		buf := make([]byte, 8192)
		for {
			n, err := wsConn.Read(buf)
			if err != nil {
				break
			}
			r.mu.Lock()
			ag := sess.AgentConn
			r.mu.Unlock()
			if ag != nil {
				_, _ = ag.Write(buf[:n])
			}
		}
	}

	<-sess.Done
}
