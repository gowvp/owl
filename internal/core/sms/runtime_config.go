package sms

// runtimeMediaServer overlays the deployment-owned receive address on a copy.
// Bootstrap is the source of truth for the default node (also at startup).
// Reading a committed snapshot avoids stale DB/cache copies and a second write
// transaction during initialization. Other media nodes retain their own values.
func (n *NodeManager) runtimeMediaServer(server *MediaServer) *MediaServer {
	if server == nil || n == nil || n.config == nil || server.ID != DefaultMediaServerID {
		return server
	}
	out := *server
	out.SDPIP = n.config.MediaSnapshot().SDPIP
	return &out
}
