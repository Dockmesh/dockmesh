package agents

// NewLoopback returns an agent session without a socket, for testing
// server code against agent code in one process. serve gets every frame
// the server sends; frames passed to reply are routed back as the read
// loop routes frames from a real agent. The session ends with stop.
func NewLoopback(id string, hello HelloPayload, serve func(f Frame, reply func(Frame))) (ag *ConnectedAgent, stop func()) {
	ag = newConnectedAgent(id, id, nil)
	ag.Hello = hello
	reply := func(f Frame) {
		if f.Type == FrameRes {
			ag.deliverResponse(f)
			return
		}
		ag.routeStreamFrame(f)
	}
	go func() {
		for {
			select {
			case <-ag.done:
				return
			case f := <-ag.send:
				go serve(f, reply)
			}
		}
	}()
	return ag, ag.shutdown
}
