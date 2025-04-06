package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"text/template"
	"time"

	hand "webrtc-app/internal/handlers"

	"github.com/pion/logging"
	// verifytoken "webrtc-app/test-verify-token"
)

var (
	// indexTemplate = &template.Template{}
	log = logging.NewDefaultLoggerFactory().NewLogger("sfu-ws")
)

func main() {
	flag.Parse()

	// Контекст для graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Загрузка статики
	indexHTML, err := os.ReadFile("internal/static/index.html")
	if err != nil {
		fmt.Printf("Failed to read index.html: %v", err)
	}

	styleCSS, err := os.ReadFile("internal/static/style.css")
	if err != nil {
		fmt.Printf("Failed to read style.css: %v", err)
	}

	scriptJS, err := os.ReadFile("internal/static/script.js")
	if err != nil {
		fmt.Printf("Failed to read script.js: %v", err)
	}

	indexTemplate := template.Must(template.New("").Parse(string(indexHTML)))

	// Общий тикер
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				hand.RoomsLock.RLock()
				for _, room := range hand.Rooms {
					room.DispatchKeyFrame()
				}
				hand.RoomsLock.RUnlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/create-room", hand.EnableCORS(hand.CreateRoomHandler))
	mux.HandleFunc("/api/check-room", hand.EnableCORS(hand.CheckRoomHandler))
	mux.HandleFunc("/websocket", hand.EnableCORS(hand.WebsocketHandler))

	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write(styleCSS)
	})

	mux.HandleFunc("/script.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(scriptJS)
	})

	mux.HandleFunc("/", hand.EnableCORS(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		if err := indexTemplate.Execute(w, "ws://"+r.Host+"/websocket"); err != nil {
			log.Errorf("Failed to execute template: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}))

	server := &http.Server{
		Addr:    *hand.Addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			fmt.Printf("HTTP server shutdown error: %v", err)
		}
	}()

	log.Infof("Server starting on %s", *hand.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Printf("HTTP server failed: %v", err)
	}

	log.Info("Server stopped gracefully")
}
