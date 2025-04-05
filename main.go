package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"sync"
	"text/template"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// nolint
var (
	addr     = flag.String("addr", ":8080", "http service address")
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	indexTemplate = &template.Template{}

	// блокировка для peerConnections и trackLocals
	// listLock        sync.RWMutex
	// peerConnections []peerConnectionState
	// trackLocals     map[string]*webrtc.TrackLocalStaticRTP
	rooms     = make(map[string]*Room)
	roomsLock sync.RWMutex

	log = logging.NewDefaultLoggerFactory().NewLogger("sfu-ws")
)

type Room struct {
	Name        string
	Peers       []peerConnectionState
	TrackLocals map[string]*webrtc.TrackLocalStaticRTP
	ListLock    sync.RWMutex
}

func (r *Room) addTrack(t *webrtc.TrackRemote) *webrtc.TrackLocalStaticRTP {
	r.ListLock.Lock()
	defer func() {
		r.ListLock.Unlock()
		r.signalPeerConnections()
	}()

	trackLocal, err := webrtc.NewTrackLocalStaticRTP(t.Codec().RTPCodecCapability, t.ID(), t.StreamID())
	if err != nil {
		panic(err)
	}
	r.TrackLocals[t.ID()] = trackLocal
	return trackLocal
}

func (r *Room) removeTrack(t *webrtc.TrackLocalStaticRTP) {
	r.ListLock.Lock()
	defer func() {
		r.ListLock.Unlock()
		r.signalPeerConnections()
	}()
	delete(r.TrackLocals, t.ID())
}

func (r *Room) signalPeerConnections() {
	r.ListLock.Lock()
	defer func() {
		r.ListLock.Unlock()
		r.dispatchKeyFrame()
	}()

	attemptSync := func() bool {
		for i := 0; i < len(r.Peers); {
			pcState := &r.Peers[i]
			if pcState.peerConnection.ConnectionState() == webrtc.PeerConnectionStateClosed {
				r.Peers = append(r.Peers[:i], r.Peers[i+1:]...)
				continue
			}

			existingSenders := map[string]bool{}

			for _, sender := range pcState.peerConnection.GetSenders() {
				if sender.Track() == nil {
					continue
				}
				existingSenders[sender.Track().ID()] = true
				if _, ok := r.TrackLocals[sender.Track().ID()]; !ok {
					if err := pcState.peerConnection.RemoveTrack(sender); err != nil {
						return true
					}
				}
			}

			for _, receiver := range pcState.peerConnection.GetReceivers() {
				if receiver.Track() == nil {
					continue
				}
				existingSenders[receiver.Track().ID()] = true
			}

			for trackID := range r.TrackLocals {
				if _, ok := existingSenders[trackID]; !ok {
					if _, err := pcState.peerConnection.AddTrack(r.TrackLocals[trackID]); err != nil {
						return true
					}
				}
			}

			offer, err := pcState.peerConnection.CreateOffer(nil)
			if err != nil {
				return true
			}
			if err = pcState.peerConnection.SetLocalDescription(offer); err != nil {
				return true
			}

			offerString, err := json.Marshal(offer)
			if err != nil {
				log.Errorf("Failed to marshal offer to json: %v", err)
				return true
			}

			if err = pcState.websocket.WriteJSON(&websocketMessage{
				Event: "offer",
				Data:  string(offerString),
			}); err != nil {
				return true
			}

			i++
		}
		return false
	}

	for syncAttempt := 0; ; syncAttempt++ {
		if syncAttempt == 25 {
			go func() {
				time.Sleep(time.Second * 3)
				r.signalPeerConnections()
			}()
			return
		}
		if !attemptSync() {
			break
		}
	}
}

func (r *Room) dispatchKeyFrame() {
	r.ListLock.Lock()
	defer r.ListLock.Unlock()
	for i := range r.Peers {
		for _, receiver := range r.Peers[i].peerConnection.GetReceivers() {
			if receiver.Track() == nil {
				continue
			}
			_ = r.Peers[i].peerConnection.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{MediaSSRC: uint32(receiver.Track().SSRC())},
			})
		}
	}
}

// type websocketMessage struct {
// 	Event string `json:"event"`
// 	Data  string `json:"data"`
// }

type websocketMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
	// Добавляем поля для чата
	Sender string `json:"sender,omitempty"`
	Text   string `json:"text,omitempty"`
}

type peerConnectionState struct {
	peerConnection *webrtc.PeerConnection
	websocket      *threadSafeWriter
}

func main() {
	flag.Parse()
	go func() {
		for range time.NewTicker(time.Second * 3).C {
			roomsLock.RLock()
			for _, room := range rooms {
				room.dispatchKeyFrame()
			}
			roomsLock.RUnlock()
		}
	}()

	// Init other state
	// trackLocals = map[string]*webrtc.TrackLocalStaticRTP{}

	// Read index.html from disk into memory, serve whenever anyone requests /
	indexHTML, err := os.ReadFile("index.html")
	if err != nil {
		panic(err)
	}
	indexTemplate = template.Must(template.New("").Parse(string(indexHTML)))

	// websocket handler
	http.HandleFunc("/websocket", websocketHandler)

	// index.html handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if err = indexTemplate.Execute(w, "ws://"+r.Host+"/websocket"); err != nil {
			log.Errorf("Failed to parse index template: %v", err)
		}
	})

	// start HTTP server
	log.Infof("Server started on: %s", *addr)
	if err = http.ListenAndServe(*addr, nil); err != nil { //nolint: gosec
		log.Errorf("Failed to start http server: %v", err)
	}
}

// Handle incoming websockets
// Handle incoming websockets
func websocketHandler(w http.ResponseWriter, r *http.Request) {
	roomName := r.URL.Query().Get("room")
	if roomName == "" {
		http.Error(w, "missing room", http.StatusBadRequest)
		return
	}

	roomsLock.Lock()
	room, ok := rooms[roomName]
	if !ok {
		room = &Room{
			Name:        roomName,
			TrackLocals: make(map[string]*webrtc.TrackLocalStaticRTP),
		}
		rooms[roomName] = room
	}
	roomsLock.Unlock()
	// Upgrade HTTP request to Websocket
	unsafeConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Errorf("Failed to upgrade HTTP to Websocket: ", err)
		return
	}

	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}

	// Create new PeerConnection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Errorf("Failed to creates a PeerConnection: %v", err)
		c.Close()
		return
	}

	// When this frame returns close the PeerConnection
	defer peerConnection.Close() //nolint

	// Принимает одну аудио- и одну видеодорожку входящего сигнала
	for _, typ := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := peerConnection.AddTransceiverFromKind(typ, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionRecvonly,
		}); err != nil {
			log.Errorf("Failed to add transceiver: %v", err)
			c.Close()
			return
		}
	}

	// Добавьте наш новый PeerConnection в глобальный список
	roomsLock.Lock()
	room.Peers = append(room.Peers, peerConnectionState{peerConnection, c})
	roomsLock.Unlock()

	// Trickle ICE. Передача кандидата сервера клиенту
	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		if i == nil {
			return
		}
		// Если вы сериализуете кандидата, обязательно используйте ToJSON
		// Использование Marshal приведет к ошибкам вокруг `sdpMid`
		candidateString, err := json.Marshal(i.ToJSON())
		if err != nil {
			log.Errorf("Failed to marshal candidate to json: %v", err)
			return
		}

		log.Infof("Send candidate to client: %s", candidateString)

		if writeErr := c.WriteJSON(&websocketMessage{
			Event: "candidate",
			Data:  string(candidateString),
		}); writeErr != nil {
			log.Errorf("Failed to write JSON: %v", writeErr)
		}
	})

	// Если PeerConnection закрыт, удалите его из глобального списка.
	peerConnection.OnConnectionStateChange(func(p webrtc.PeerConnectionState) {
		log.Infof("Connection state change: %s", p)

		switch p {
		case webrtc.PeerConnectionStateFailed:
			if err := peerConnection.Close(); err != nil {
				log.Errorf("Failed to close PeerConnection: %v", err)
			}
		case webrtc.PeerConnectionStateClosed:
			room.signalPeerConnections()
		default:
		}
	})

	peerConnection.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Infof("Got remote track: Kind=%s, ID=%s, PayloadType=%d", t.Kind(), t.ID(), t.PayloadType())

		// Create a track to fan out our incoming video to all peers
		trackLocal := room.addTrack(t)
		defer room.removeTrack(trackLocal)

		buf := make([]byte, 1500)
		rtpPkt := &rtp.Packet{}

		for {
			i, _, err := t.Read(buf)
			if err != nil {
				return
			}

			if err = rtpPkt.Unmarshal(buf[:i]); err != nil {
				log.Errorf("Failed to unmarshal incoming RTP packet: %v", err)
				return
			}

			rtpPkt.Extension = false
			rtpPkt.Extensions = nil

			if err = trackLocal.WriteRTP(rtpPkt); err != nil {
				return
			}
		}
	})

	peerConnection.OnICEConnectionStateChange(func(is webrtc.ICEConnectionState) {
		log.Infof("ICE connection state changed: %s", is)
	})

	// Signal for the new PeerConnection
	room.signalPeerConnections()

	message := &websocketMessage{}
	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Errorf("WebSocket read error: %v", err)
			}
			break
		}

		log.Infof("Got message: %s", raw)

		if err := json.Unmarshal(raw, &message); err != nil {
			log.Errorf("Failed to unmarshal json to message: %v", err)
			continue // Продолжаем обработку даже при ошибке unmarshal
		}

		switch message.Event {
		case "candidate":
			candidate := webrtc.ICECandidateInit{}
			if err := json.Unmarshal([]byte(message.Data), &candidate); err != nil {
				log.Errorf("Failed to unmarshal json to candidate: %v", err)
				continue
			}

			log.Infof("Got candidate: %v", candidate)

			if err := peerConnection.AddICECandidate(candidate); err != nil {
				log.Errorf("Failed to add ICE candidate: %v", err)
				continue
			}
		case "answer":
			answer := webrtc.SessionDescription{}
			if err := json.Unmarshal([]byte(message.Data), &answer); err != nil {
				log.Errorf("Failed to unmarshal json to answer: %v", err)
				continue
			}

			log.Infof("Got answer: %v", answer)

			if err := peerConnection.SetRemoteDescription(answer); err != nil {
				log.Errorf("Failed to set remote description: %v", err)
				continue
			}
		case "chat": // Добавляем обработку сообщений чата
			// Рассылаем сообщение всем участникам комнаты
			room.ListLock.RLock()
			for _, peer := range room.Peers {
				if err := peer.websocket.WriteJSON(message); err != nil {
					log.Errorf("Failed to send chat message: %v", err)
				}
			}
			room.ListLock.RUnlock()
		default:
			log.Errorf("unknown message: %+v", message)
		}
	}
}

// Helper to make Gorilla Websockets threadsafe
type threadSafeWriter struct {
	*websocket.Conn
	sync.Mutex
}

func (t *threadSafeWriter) WriteJSON(v interface{}) error {
	t.Lock()
	defer t.Unlock()

	return t.Conn.WriteJSON(v)
}
