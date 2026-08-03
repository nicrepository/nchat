package ws

import "context"

func (h *Hub) handleUserBroadcast(req broadcastReq) {
	var slow []*Client
	h.mu.Lock()
	for _, client := range h.clients {
		if client.workspaceID != req.event.WorkspaceID || client.userID != req.event.TargetID {
			continue
		}
		if !client.enqueue(req.data) {
			slow = append(slow, client)
		}
	}
	h.mu.Unlock()

	for _, client := range slow {
		h.logger.WarnContext(context.Background(), "ws: dropping slow call client", "user_id", client.userID)
		h.Unregister(client)
		client.close()
	}
}
