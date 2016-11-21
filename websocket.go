package turnpike

import (
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WSMessage struct {
	MessageType	int
	MessageData	[]byte
}

type websocketPeer struct {
	conn        *websocket.Conn
	serializer  Serializer
	messages    chan Message
	send 	    chan WSMessage
	payloadType int
	closed      bool
	sendMutex   sync.Mutex
}

// NewWebsocketPeer connects to the websocket server at the specified url.
func NewWebsocketPeer(serialization Serialization, url, origin string, tlscfg *tls.Config) (Peer, error) {
	switch serialization {
	case JSON:
		return newWebsocketPeer(url, jsonWebsocketProtocol, origin,
			new(JSONSerializer), websocket.TextMessage, tlscfg,
		)
	case MSGPACK:
		return newWebsocketPeer(url, msgpackWebsocketProtocol, origin,
			new(MessagePackSerializer), websocket.BinaryMessage, tlscfg,
		)
	default:
		return nil, fmt.Errorf("Unsupported serialization: %v", serialization)
	}
}

func newWebsocketPeer(url, protocol, origin string, serializer Serializer, payloadType int, tlscfg *tls.Config) (Peer, error) {
	dialer := websocket.Dialer{
		Subprotocols:    []string{protocol},
		TLSClientConfig: tlscfg,
	}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	ep := &websocketPeer{
		conn:        conn,
		messages:    make(chan Message, 10),
		send: 	     make(chan WSMessage, 10),
		serializer:  serializer,
		payloadType: payloadType,
	}
	go ep.run()
	go ep.writePump()

	return ep, nil
}

func (ep *websocketPeer) Send(msg Message) error {
	b, err := ep.serializer.Serialize(msg)
	if err != nil {
		return err
	}
	if !ep.closed {
		ep.send <- WSMessage{MessageType:ep.payloadType, MessageData: b}
	}

	return nil
}


func (ep *websocketPeer) Receive() <-chan Message {
	return ep.messages
}
func (ep *websocketPeer) Close() error {
	closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "goodbye")
	err := ep.conn.WriteControl(websocket.CloseMessage, closeMsg, time.Now().Add(5*time.Second))
	if err != nil {
		log.Println("error sending close message:", err)
	}

	ep.closed = true
	return ep.conn.Close()
}

const (

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	writeWait = 10 * time.Second
)


func (ep *websocketPeer) write(mt int, payload []byte) error {
	ep.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return ep.conn.WriteMessage(mt, payload)
}


func (ep *websocketPeer) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
	}()
	for {
		select {
		case message, ok := <-ep.send:
			if !ok {
				// The hub closed the channel.
				ep.write(websocket.CloseMessage, []byte{})
				log.Println("Error recieving SEND message from pool")
				close(ep.send)
				return
			}

			ep.conn.SetWriteDeadline(time.Now().Add(writeWait))
			w, err := ep.conn.NextWriter(message.MessageType)
			if err != nil {
				log.Println("Error sending msg! ",err.Error())
				return
			}
			w.Write(message.MessageData)

		// Add queued chat messages to the current websocket message.
		/*	n := len(ep.send)
			for i := 0; i < n; i++ {
				w.Write(newline)
				w.Write(<-ep.send)
			}
		*/
			if err := w.Close(); err != nil {
				log.Println("Error closing wrriter ",err.Error())
				return
			}
		case <-ticker.C:
			if err := ep.write(websocket.PingMessage, []byte{}); err != nil {
				return
			}
		}
	}
}

func (ep *websocketPeer) run() {
	ep.conn.SetPongHandler(func(string) error {  return nil })

	for {
		// TODO: use conn.NextMessage() and stream
		// TODO: do something different based on binary/text frames
		if msgType, b, err := ep.conn.ReadMessage(); err != nil {
			if ep.closed {
				log.Println("peer connection closed")
			} else {
				log.Println("error reading from peer:", err)
				ep.conn.Close()
			}
			close(ep.messages)

			break
		} else if msgType == websocket.CloseMessage {
			ep.conn.Close()
			close(ep.messages)

			break
		} else {
			msg, err := ep.serializer.Deserialize(b)
			if err != nil {
				log.Println("error deserializing peer message:", err)
				// TODO: handle error
			} else {
				ep.messages <- msg
			}
		}
	}
}

