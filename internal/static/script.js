let pc;
let ws;
let username = '';
let currentRoom = '';
let localStream;
const userVideos = {};
// Autofill fields from URL parameters
window.addEventListener('DOMContentLoaded', () => {
    const urlParams = new URLSearchParams(window.location.search);
    const room = urlParams.get('room');
    const password = urlParams.get('password');
    if (room) {
        document.getElementById('roomName').value = room;
    }
    if (password) {
        document.getElementById('roomPassword').value = password;
    }
});

function joinRoom() {
    const roomName = document.getElementById('roomName').value.trim();
    const password = document.getElementById('roomPassword').value.trim();
    username = document.getElementById('username').value.trim() || 'Guest';
    if (!roomName || !password) {
        alert("Please enter room name and password");
        return;
    }
    updateStatus("Connecting to room...");
    // First verify the room password
    fetch('/api/check-room', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
        },
        body: JSON.stringify({
            name: roomName,
            password: password,
            username: username
        })
    }).then(response => {
        if (!response.ok) {
            return response.json().then(err => {
                throw err;
            });
        }
        return response.json();
    }).then(data => {
        if (data.status === 'success') {
            currentRoom = roomName;
            document.getElementById('chatHeader').textContent = `Чат: ${roomName}`;
            document.getElementById('chatMessages').innerHTML = '';
            // Hide join form and show leave button
            document.getElementById('joinForm').style.display = 'none';
            document.getElementById('leaveBtn').style.display = 'block';
            connectToRoom(roomName, password, username);
        }
    }).catch(error => {
        console.error('Error:', error);
        updateStatus("Failed to join room");
        alert(error.error || "Failed to join room");
    });
}

function leaveRoom() {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.close();
    }
    if (pc) {
        pc.close();
    }
    if (localStream) {
        localStream.getTracks().forEach(track => track.stop());
    }
    // Clear all remote videos
    const videoGrid = document.getElementById('videoGrid');
    while (videoGrid.children.length > 1) {
        videoGrid.removeChild(videoGrid.lastChild);
    }
    // Reset UI
    document.getElementById('joinForm').style.display = 'block';
    document.getElementById('leaveBtn').style.display = 'none';
    document.getElementById('localVideo').srcObject = null;
    updateStatus("Disconnected");
    currentRoom = '';
    username = '';
    userVideos = {};
}

function connectToRoom(roomName, password, username) {
    const protocol = location.protocol === "https:" ? "wss" : "ws";
    const wsURL = `${protocol}://${location.host}/websocket?room=${encodeURIComponent(roomName)}&username=${encodeURIComponent(username)}&password=${encodeURIComponent(password)}`;
    startConnection(wsURL);
}

function startConnection(wsURL) {
    navigator.mediaDevices.getUserMedia({
        video: {
            width: {
                ideal: 1280
            },
            height: {
                ideal: 720
            }
        },
        audio: true
    }).then(stream => {
        localStream = stream;
        pc = new RTCPeerConnection({
            iceServers: [{
                urls: 'stun:stun.l.google.com:19302'
            }]
        });
        // Clear previous remote videos
        const videoGrid = document.getElementById('videoGrid');
        while (videoGrid.children.length > 1) {
            videoGrid.removeChild(videoGrid.lastChild);
        }
        pc.ontrack = function(event) {
            if (event.track.kind === 'audio') return;
            const streamUsername = event.track.id.split('_')[0] || 'Participant';
            if (userVideos[streamUsername]) {
                userVideos[streamUsername].video.srcObject = event.streams[0];
                return;
            }
            const videoItem = document.createElement('div');
            videoItem.className = 'video-item';
            const video = document.createElement('video');
            video.autoplay = true;
            video.playsInline = true;
            videoItem.appendChild(video);
            document.getElementById('videoGrid').appendChild(videoItem);
            video.srcObject = event.streams[0];
            userVideos[streamUsername] = {
                element: videoItem,
                video: video
            };
            event.track.onmute = () => {
                video.play();
            };
            event.streams[0].onremovetrack = ({
                track
            }) => {
                if (videoItem.parentNode) {
                    videoItem.parentNode.removeChild(videoItem);
                    delete userVideos[streamUsername];
                }
            };
        };
        document.getElementById('localVideo').srcObject = stream;
        stream.getTracks().forEach(track => {
            track.id = `${username}_${track.id}`;
            pc.addTrack(track, stream);
        });
        ws = new WebSocket(wsURL);
        pc.onicecandidate = e => {
            if (e.candidate) {
                ws.send(JSON.stringify({
                    event: 'candidate',
                    data: JSON.stringify(e.candidate)
                }));
            }
        };
        ws.onclose = () => {
            updateStatus("Disconnected from room");
        };
        ws.onerror = evt => {
            updateStatus("Connection error");
            console.error("WebSocket error:", evt);
        };
        ws.onmessage = evt => {
            const msg = JSON.parse(evt.data);
            if (!msg) {
                console.error("Failed to parse WebSocket message");
                return;
            }
            switch (msg.event) {
                case 'offer':
                    const offer = JSON.parse(msg.data);
                    pc.setRemoteDescription(offer).then(() => pc.createAnswer()).then(answer => {
                        pc.setLocalDescription(answer);
                        ws.send(JSON.stringify({
                            event: 'answer',
                            data: JSON.stringify(answer)
                        }));
                        updateStatus("Подключено к заседанию: " + currentRoom);
                    }).catch(err => {
                        console.error("Error handling offer:", err);
                        updateStatus("Connection error");
                    });
                    break;
                case 'candidate':
                    const candidate = JSON.parse(msg.data);
                    pc.addIceCandidate(new RTCIceCandidate(candidate)).catch(err => {
                        console.error("Error adding ICE candidate:", err);
                    });
                    break;
                case 'chat':
                    addChatMessage(msg.sender, msg.text);
                    break;
                case 'chat_history':
                    try {
                        const history = JSON.parse(msg.data);
                        history.forEach(item => {
                            addChatMessage(item.sender, item.text, new Date(item.timestamp));
                        });
                    } catch (err) {
                        console.error("Error parsing chat history:", err);
                    }
                    break;
                default:
                    console.log("Unknown message type:", msg.event);
            }
        };
        ws.onopen = () => {
            updateStatus("Connected to room: " + currentRoom);
        };
    }).catch(err => {
        updateStatus("Media access error");
        alert("Error accessing media devices: " + err.message);
        console.error("Media error:", err);
    });
}

function addChatMessage(sender, text, timestamp = new Date()) {
    const chatDiv = document.getElementById('chatMessages');
    const messageDiv = document.createElement('div');
    messageDiv.className = 'message';
    const messageHeader = document.createElement('div');
    messageHeader.className = 'message-header';
    const senderSpan = document.createElement('span');
    senderSpan.className = 'message-sender';
    senderSpan.textContent = sender;
    const timeSpan = document.createElement('span');
    timeSpan.className = 'message-time';
    timeSpan.textContent = timestamp.toLocaleTimeString([], {
        hour: '2-digit',
        minute: '2-digit'
    });
    const messageText = document.createElement('div');
    messageText.textContent = text;
    messageHeader.appendChild(senderSpan);
    messageHeader.appendChild(timeSpan);
    messageDiv.appendChild(messageHeader);
    messageDiv.appendChild(messageText);
    chatDiv.appendChild(messageDiv);
    chatDiv.scrollTop = chatDiv.scrollHeight;
}

function sendChatMessage() {
    const message = document.getElementById('chatInput').value.trim();
    if (message === '') return;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
        alert("Not connected to the room yet");
        return;
    }
    ws.send(JSON.stringify({
        event: 'chat',
        sender: username,
        text: message
    }));
    document.getElementById('chatInput').value = '';
}

function updateStatus(message) {
    document.getElementById('statusBar').textContent = message;
}
// Handle Enter key in chat input
document.getElementById('chatInput').addEventListener('keypress', function(e) {
    if (e.key === 'Enter') {
        sendChatMessage();
    }
});