package gochat

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 10000

	TypeUserActivityChannelConnect = "user-channel-connect"
	TypeUserActivityGroupJoin      = "user-group-join"
	TypeUserActivityGroupLeave     = "user-group-leave"
	TypeUserActivityMessageSend    = "user-message-send"
	TypeUserActivityDisconnect     = "user-disconnect"
)

var (
	newline = []byte{'\n'}
	// space   = []byte{' '}
)

type (
	UserActivity struct {
		Type    string
		Message *Message
	}

	User struct {
		ID             string            `json:"id"`
		Name           string            `json:"name"`
		AdditionalInfo map[string]string `json:"additionalInfo,omitempty"`
		conn           *websocket.Conn
		server         *Server
		channel        *Channel
		groups         map[string]*Group
		send           chan []byte
		activity       chan *UserActivity
	}
)

func NewUser(conn *websocket.Conn, server *Server) *User {
	return &User{
		conn:     conn,
		server:   server,
		groups:   make(map[string]*Group),
		send:     make(chan []byte, 256),
		activity: make(chan *UserActivity),
	}
}

func (user *User) ReadPump() {
	defer func() {
		user.handleUserdisconnect()
	}()

	user.conn.SetReadLimit(maxMessageSize)
	user.conn.SetReadDeadline(time.Now().Add(pongWait))
	user.conn.SetPongHandler(func(string) error { user.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })

	for {
		_, jsonMessage, err := user.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("unexpected close error: %v", err)
			}
			break
		}

		var message Message
		if err := json.Unmarshal(jsonMessage, &message); err != nil {
			log.Printf("Error on unmarshal JSON message %s", err)
			return
		}

		switch message.Command {
		case CommandUserConnect:
			user.SetActivity(TypeUserActivityChannelConnect, &message)

			user.handleUserConnect(message)
		case CommandMessageSend:
			user.SetActivity(TypeUserActivityMessageSend, &message)

			if user.channel != nil {
				user.handleSendMessage(message)
			}
		case CommandGroupJoin:
			user.SetActivity(TypeUserActivityGroupJoin, &message)

			if user.channel != nil {
				user.handleGroupJoin(message)
			}
		case CommandGroupLeave:
			user.SetActivity(TypeUserActivityGroupLeave, &message)

			if user.channel != nil {
				user.handleGroupLeave(message)
			}
		}
	}

}

func (user *User) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		user.conn.Close()
	}()

	for {
		select {
		case message, ok := <-user.send:
			user.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The Server closed the channel.
				user.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := user.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Attach queued chat messages to the current websocket message.
			n := len(user.send)
			for i := 0; i < n; i++ {
				w.Write(newline)
				w.Write(<-user.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			user.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := user.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (user *User) GetActivity() chan *UserActivity {
	return user.activity
}

func (user *User) SetActivity(activityType string, message *Message) {
	user.activity <- &UserActivity{
		Type:    activityType,
		Message: message,
	}
}

func (user *User) handleUserConnect(message Message) {
	if message.User != nil && message.Channel != nil {
		user.ID = message.User.ID
		user.Name = message.User.Name
		user.AdditionalInfo = message.User.AdditionalInfo

		user.channel = user.server.findChannelByID(message.Channel.ID)
		if user.channel == nil {
			user.channel = NewChannel(
				message.Channel.ID,
				message.Channel.Name,
				message.Channel.AdditionalInfo,
			)

			user.server.registerChannel <- user.channel

			go user.channel.Run()
		}

		user.channel.registerUser <- user

		message.Message = &MessageInfo{
			Type: TypeMessageText,
			Text: MessageUserConnectSuccessful,
		}

		message.Response = &ResponseInfo{
			Status:  true,
			Message: ResponseMessageSuccess,
		}
	} else {
		message.Response = &ResponseInfo{
			Status:  false,
			Message: ResponseMessageInvalidPayload,
		}
	}

	user.send <- []byte(message.encode())
}

func (user *User) handleUserdisconnect() {
	user.SetActivity(TypeUserActivityDisconnect, nil)

	if user.channel != nil {
		user.channel.unregisterUser <- user
	}

	close(user.send)
	user.conn.Close()
}

func (user *User) handleGroupJoin(message Message) {
	if message.Group != nil {
		group := user.channel.findGroupByID(message.Group.ID)
		if group == nil {
			group = NewGroup(
				message.Group.ID,
				message.Group.Name,
				message.Group.AdditionalInfo,
			)

			go group.Run()

		}

		user.channel.registerGroup <- group
		group.registerUser <- user
		user.groups[group.ID] = group

		message.User = user
		message.Message = &MessageInfo{
			Type: TypeMessageText,
			Text: MessageGroupJoin,
		}
		message.Response = &ResponseInfo{
			Status:  true,
			Message: ResponseMessageSuccess,
		}

		user.send <- []byte(message.encode())
	}
}

func (user *User) handleGroupLeave(message Message) {
	if message.Group != nil {
		group := user.channel.findGroupByID(message.Group.ID)
		if group != nil {
			delete(user.groups, user.ID)
			group.unregisterUser <- user

			if len(group.users) == 0 {
				user.channel.unregisterGroup <- group
			}

			message.User = user
			message.Group = group
			message.Response = &ResponseInfo{
				Status:  true,
				Message: ResponseMessageSuccess,
			}

			user.send <- []byte(message.encode())
		}

	} else {
		message.Response = &ResponseInfo{
			Status:  false,
			Message: ResponseMessageInvalidPayload,
		}

		user.send <- []byte(message.encode())
	}
}

func (user *User) handleSendMessage(message Message) {
	if message.Message != nil && message.Target != nil {
		switch message.Target.Type {
		case TypeTargetDirect:
			user.handleSendDirectMessage(message)
		case TypeTargetGroup:
			user.handlerSendGroupMessage(message)
		}
	} else {
		message.Response = &ResponseInfo{
			Status:  false,
			Message: ResponseMessageInvalidPayload,
		}

		user.send <- []byte(message.encode())
	}
}

func (user *User) handleSendDirectMessage(message Message) {
	if message.Target.User != nil {
		userTarget := user.channel.findUserByID(message.Target.User.ID)
		if userTarget == nil {
			message.Response = &ResponseInfo{
				Status:  false,
				Message: ResponseMessageUserTargetNotConnected,
			}

			message.User = user

			user.send <- []byte(message.encode())
		} else {
			userTarget.send <- []byte(message.encode())

			message.User = user
			message.Target.User = userTarget
			message.Response = &ResponseInfo{
				Status:  true,
				Message: ResponseMessageSuccess,
			}

			user.send <- []byte(message.encode())
		}
	}
}

func (user *User) handlerSendGroupMessage(message Message) {
	if message.Target.Group != nil {
		groupTarget := user.channel.findGroupByID(message.Target.Group.ID)
		if groupTarget != nil {
			message.User = user
			message.Target.Group = groupTarget

			for _, userGroupTarget := range groupTarget.users {
				if userGroupTarget.ID != user.ID {
					userGroupTarget.send <- []byte(message.encode())
				}
			}

			message.Response = &ResponseInfo{
				Status:  true,
				Message: ResponseMessageSuccess,
			}

			user.send <- []byte(message.encode())
		}
	}
}
