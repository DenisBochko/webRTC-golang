package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"text/template"
	"time"
	verifytoken "webrtc-app/test-verify-token"

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

	rooms         = make(map[string]*Room)
	roomPasswords = make(map[string]string) // Хранилище паролей комнат
	roomsLock     sync.RWMutex

	log = logging.NewDefaultLoggerFactory().NewLogger("sfu-ws")
)

// enableCORS добавляет CORS заголовки к ответу
func enableCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

		// Предварительный запрос (preflight) для CORS
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// Структуры запросов для API
type CreateRoomRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type JoinRoomRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Username string `json:"username"`
}

// ChatMessage представляет сообщение в чате
type ChatMessage struct {
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

type Room struct {
	Name        string
	Peers       []peerConnectionState
	TrackLocals map[string]*webrtc.TrackLocalStaticRTP
	ChatHistory []ChatMessage
	ListLock    sync.RWMutex
}

// Добавляем метод для добавления сообщения в историю чата
func (r *Room) addChatMessage(sender, text string) {
	r.ListLock.Lock()
	defer r.ListLock.Unlock()

	message := ChatMessage{
		Sender:    sender,
		Text:      text,
		Timestamp: time.Now(),
	}

	r.ChatHistory = append(r.ChatHistory, message)

	// Ограничиваем размер истории (например, последние 100 сообщений)
	if len(r.ChatHistory) > 100 {
		r.ChatHistory = r.ChatHistory[len(r.ChatHistory)-100:]
	}
}

// Добавляем метод для отправки истории чата новому участнику
func (r *Room) sendChatHistory(ws *threadSafeWriter) error {
	r.ListLock.RLock()
	defer r.ListLock.RUnlock()

	if len(r.ChatHistory) == 0 {
		return nil
	}

	historyMessage := websocketMessage{
		Event:  "chat_history",
		Sender: "system",
		Data:   "", // Данные будут в отдельном поле
	}

	// Преобразуем историю в JSON
	historyJSON, err := json.Marshal(r.ChatHistory)
	if err != nil {
		return err
	}

	// Используем поле Data для истории
	historyMessage.Data = string(historyJSON)

	return ws.WriteJSON(&historyMessage)
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
	Event  string `json:"event"`
	Data   string `json:"data"`
	Sender string `json:"sender,omitempty"`
	Text   string `json:"text,omitempty"`
}

type peerConnectionState struct {
	peerConnection *webrtc.PeerConnection
	websocket      *threadSafeWriter
	username       string // Добавляем имя пользователя
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

	// Read index.html from disk into memory
	indexHTML, err := os.ReadFile("static/index.html")
	if err != nil {
		panic(err)
	}
	indexTemplate = template.Must(template.New("").Parse(string(indexHTML)))

	// API endpoints с CORS
	http.HandleFunc("/api/create-room", enableCORS(createRoomHandler))
	http.HandleFunc("/api/check-room", enableCORS(checkRoomHandler))
	http.HandleFunc("/websocket", enableCORS(websocketHandler))
	http.HandleFunc("/", enableCORS(func(w http.ResponseWriter, r *http.Request) {
		// верифицируем токен пользователя
		// рабочик токен Token 4b4d65e2c6987c60be6231febe98a064b7167ae4
		authToken := r.Header.Get("Authorization")
		validauthToken, _ := verifytoken.ValidateToken(authToken)
		fmt.Println(authToken)
		if !validauthToken {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		if err = indexTemplate.Execute(w, "ws://"+r.Host+"/websocket"); err != nil {
			log.Errorf("Failed to parse index template: %v", err)
		}
	}))

	log.Infof("Server started on: %s", *addr)
	if err = http.ListenAndServe(*addr, nil); err != nil {
		log.Errorf("Failed to start http server: %v", err)
	}
}

// Обработчик создания комнаты
func createRoomHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CreateRoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Password == "" {
		http.Error(w, "Room name and password are required", http.StatusBadRequest)
		return
	}

	roomsLock.Lock()
	defer roomsLock.Unlock()

	if _, exists := rooms[req.Name]; exists {
		http.Error(w, "Room already exists", http.StatusConflict)
		return
	}

	// Создаем комнату
	room := &Room{
		Name:        req.Name,
		TrackLocals: make(map[string]*webrtc.TrackLocalStaticRTP),
		ChatHistory: make([]ChatMessage, 0),
	}
	rooms[req.Name] = room
	roomPasswords[req.Name] = req.Password

	w.Header().Set("Content-Type", "application/json")
	// "uri": fmt.Sprintf("https://3449009-eq23140.twc1.net/?room=%s&password=%s",
	// 		req.Name, req.Password),
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "success",
		"room":     req.Name,
		"password": req.Password,
		"uri": fmt.Sprintf("http://localhost:8080/?room=%s&password=%s",
			req.Name, req.Password),
	})
}

// Обработчик проверки комнаты
func checkRoomHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req JoinRoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	roomsLock.RLock()
	defer roomsLock.RUnlock()

	password, exists := roomPasswords[req.Name]
	if !exists {
		http.Error(w, "Room does not exist", http.StatusNotFound)
		return
	}

	if password != req.Password {
		http.Error(w, "Invalid password", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "success",
		"room":   req.Name,
	})
}

// Handle incoming websockets
// Модифицированный websocketHandler с проверкой пароля
func websocketHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	roomName := r.URL.Query().Get("room")
	username := r.URL.Query().Get("username")
	password := r.URL.Query().Get("password")

	if roomName == "" || password == "" {
		http.Error(w, "room and password are required", http.StatusBadRequest)
		return
	}

	// Проверяем пароль комнаты
	roomsLock.RLock()
	roomPassword, roomExists := roomPasswords[roomName]
	roomsLock.RUnlock()

	if !roomExists {
		http.Error(w, "Room does not exist", http.StatusNotFound)
		return
	}

	if roomPassword != password {
		http.Error(w, "Invalid password", http.StatusUnauthorized)
		return
	}

	if username == "" {
		username = "anonymous"
	}

	// // верифицируем токен пользователя
	// // рабочик токен Token 4b4d65e2c6987c60be6231febe98a064b7167ae4
	// authToken := r.Header.Get("Authorization")
	// validauthToken, _ := verifytoken.ValidateToken(authToken)

	// if !validauthToken {
	// 	http.Error(w, "Invalid token", http.StatusUnauthorized)
	// 	return
	// }

	roomsLock.Lock()
	room, ok := rooms[roomName]
	if !ok {
		// Это не должно происходить, так как мы уже проверили roomPasswords
		http.Error(w, "Room configuration error", http.StatusInternalServerError)
		roomsLock.Unlock()
		return
	}
	roomsLock.Unlock()

	unsafeConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Errorf("Failed to upgrade HTTP to Websocket: ", err)
		return
	}

	c := &threadSafeWriter{unsafeConn, sync.Mutex{}}

	// Отправляем историю чата новому участнику
	if err := room.sendChatHistory(c); err != nil {
		log.Errorf("Failed to send chat history: %v", err)
	}

	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Errorf("Failed to creates a PeerConnection: %v", err)
		c.Close()
		return
	}

	defer peerConnection.Close()

	for _, typ := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := peerConnection.AddTransceiverFromKind(typ, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionRecvonly,
		}); err != nil {
			log.Errorf("Failed to add transceiver: %v", err)
			c.Close()
			return
		}
	}

	roomsLock.Lock()
	room.Peers = append(room.Peers, peerConnectionState{peerConnection, c, username})
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
			continue
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
		case "chat":
			// Добавляем сообщение в историю комнаты
			room.addChatMessage(message.Sender, message.Text)

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
