package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
)

const (
	defaultHttpAddr   = "127.0.0.1:61121"
	adbHost           = "tv.lan"
	adbPort           = 5555
	expectedAdbSerial = "tv.lan:5555"
)

func handleKeyEvent(w http.ResponseWriter, r *http.Request) {
	keycode := r.FormValue("keycode")
	if keycode == "" {
		http.Error(w, "keycode is required", http.StatusBadRequest)
		return
	}
	cmd := exec.Command("adb", "-s", expectedAdbSerial, "shell", "input", "keyevent", keycode)
	if err := cmd.Run(); err != nil {
		log.Printf("Failed to send key event %s: %v", keycode, err)
		http.Error(w, "Failed to send key event", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func main() {
	socketPath := flag.String("unix", "", "Path to the Unix domain socket for the HTTP server. If set, takes precedence over -http.")
	httpAddr := flag.String("http", defaultHttpAddr, "HTTP listen address (e.g., :8080). Used if -socket is not provided.")
	adbLocalServerPort := flag.Int("adb-port", 27754, "Port for the local ADB port.")
	flag.Parse()

	os.Setenv("ADB_SERVER_SOCKET", "tcp:localhost:"+strconv.Itoa(*adbLocalServerPort))
	err := exec.Command("adb", "start-server").Run()
	if err != nil {
		log.Fatalf("Failed to start ADB server: %v", err)
	}
	err = exec.Command("adb", "connect", adbHost+":"+strconv.Itoa(adbPort)).Run()
	if err != nil {
		log.Fatalf("Failed to connect to ADB server: %v", err)
	}

	service := http.NewServeMux()
	service.HandleFunc("POST /api/keyevent", handleKeyEvent)

	if *socketPath != "" {
		log.Printf("Attempting to listen on unix socket: %s", *socketPath)
		if err := os.RemoveAll(*socketPath); err != nil { // Remove socket file if it exists
			log.Fatalf("Failed to remove existing socket file %s: %v", *socketPath, err)
		}

		listener, err := net.Listen("unix", *socketPath)
		if err != nil {
			log.Fatalf("Failed to listen on unix socket %s: %v", *socketPath, err)
		}
		// Change socket permissions to be group readable/writable
		if err := os.Chmod(*socketPath, 0660); err != nil {
			log.Fatalf("Failed to change socket permissions for %s: %v", *socketPath, err)
		}

		defer listener.Close()
		defer os.RemoveAll(*socketPath) // Clean up socket file on exit

		log.Printf("Server listening on unix socket: %s", *socketPath)
		log.Fatal(http.Serve(listener, service))
	} else {
		log.Printf("Server listening on HTTP address: %s", *httpAddr)
		log.Fatal(http.ListenAndServe(*httpAddr, service))
	}
}
