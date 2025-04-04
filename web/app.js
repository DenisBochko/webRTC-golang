// Этот код JavaScript обрабатывает:
// Пользователь присоединяется к сеансу, нажав кнопку присоединения.
// Инициализация соединения WebRTC.
// Захват видео- и аудиопотоков пользователя.
// Отображение локального видеопотока.
document.getElementById('join-btn').addEventListener('click', joinSession);
document.getElementById('mute-btn').addEventListener('click', toggleMute);
document.getElementById('video-btn').addEventListener('click', toggleVideo);

let localStream;
let peerConnection;
let isMuted = false;
let isVideoStopped = false;

async function joinSession() {
    const name = document.getElementById('name').value;
    if (!name) {
        alert('Please enter your name');
        return;
    }

    document.getElementById('join-screen').style.display = 'none';
    document.getElementById('participant-view').style.display = 'block';

    peerConnection = new RTCPeerConnection({
        iceServers: [{ urls: 'stun:stun.l.google.com:19302' }]
    });
    
    localStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
    localStream.getTracks().forEach(track => peerConnection.addTrack(track, localStream));

    const localVideo = document.createElement('video');
    localVideo.srcObject = localStream;
    localVideo.autoplay = true;
    localVideo.muted = true;
    document.getElementById('videos').appendChild(localVideo);

    const ws = new WebSocket(`ws://${window.location.host}/ws`);

    ws.onopen = () => {
        console.log('Connected to the signaling server');
        ws.send(JSON.stringify({ type: 'join', name: name }));
    };

    ws.onmessage = async (message) => {
        const data = JSON.parse(message.data);
        switch (data.type) {
            case 'offer':
                await peerConnection.setRemoteDescription(new RTCSessionDescription(data.offer));
                const answer = await peerConnection.createAnswer();
                await peerConnection.setLocalDescription(answer);
                ws.send(JSON.stringify({ type: 'answer', answer: answer }));
                break;
            case 'answer':
                await peerConnection.setRemoteDescription(new RTCSessionDescription(data.answer));
                break;
            case 'candidate':
                await peerConnection.addIceCandidate(new RTCIceCandidate(data.candidate));
                break;
            default:
                break;
        }
    };

    peerConnection.onicecandidate = (event) => {
        if (event.candidate) {
            ws.send(JSON.stringify({ type: 'candidate', candidate: event.candidate }));
        }
    };

    peerConnection.ontrack = (event) => {
        addRemoteStream(event.streams[0]);
    };
}

function toggleMute() {
    localStream.getAudioTracks().forEach(track => track.enabled = !track.enabled);
    isMuted = !isMuted;
    document.getElementById('mute-btn').textContent = isMuted ? 'Unmute' : 'Mute';
}

function toggleVideo() {
    localStream.getVideoTracks().forEach(track => track.enabled = !track.enabled);
    isVideoStopped = !isVideoStopped;
    document.getElementById('video-btn').textContent = isVideoStopped ? 'Start Video' : 'Stop Video';
}

function addRemoteStream(stream) {
    const remoteVideo = document.createElement('video');
    remoteVideo.srcObject = stream;
    remoteVideo.autoplay = true;
    document.getElementById('videos').appendChild(remoteVideo);
}
