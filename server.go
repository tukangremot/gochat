package gochat

type Server struct {
	channels          map[string]*Channel
	registerChannel   chan *Channel
	unregisterChannel chan *Channel
}

func NewServer() *Server {
	return &Server{
		channels:          make(map[string]*Channel),
		registerChannel:   make(chan *Channel),
		unregisterChannel: make(chan *Channel),
	}
}

func (server *Server) Run() {
	for {
		select {
		case channel := <-server.registerChannel:
			server.handleRegisterChannel(channel)

		case channel := <-server.unregisterChannel:
			server.handleUnregisterChannel(channel)
		}

	}
}

func (server *Server) findChannelByID(channelID string) *Channel {
	if channel, ok := server.channels[channelID]; ok {
		return channel
	}

	return nil
}

func (server *Server) handleRegisterChannel(channel *Channel) {
	if _, ok := server.channels[channel.ID]; !ok {
		server.channels[channel.ID] = channel
	}
}

func (server *Server) handleUnregisterChannel(channel *Channel) {
	delete(server.channels, channel.ID)
}