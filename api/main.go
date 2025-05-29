package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const (
	defaultHttpAddr   = "127.0.0.1:61121"
	adbHost           = "tv.lan"
	adbPort           = 5555
	expectedAdbSerial = "tv.lan:5555"
)

// TVRemoteService holds the state and configuration for the remote service.
type TVRemoteService struct {
	// reconnectFunc is called when an ADB command fails, to re-establish connection.
	reconnectFunc func() error
	serveMux      http.ServeMux

	scrcpyCmd *exec.Cmd
	scrcpyMu  sync.Mutex
}

// NewTVRemoteService creates a new TVRemoteService instance.
func NewTVRemoteService(host string, port int, localServerPort int) *TVRemoteService {
	s := &TVRemoteService{}
	// Define the reconnect function for this service instance
	s.reconnectFunc = func() error {
		log.Println("Attempting ADB reconnect...")
		// ADB_SERVER_SOCKET is assumed to be set globally by main
		return startAndConnectAdb(adbHost, adbPort, localServerPort)
	}
	s.serveMux.HandleFunc("POST /api/keyevent", s.handleKeyEvent)
	s.serveMux.HandleFunc("POST /api/toggle_screen", s.handleToggleScreen)
	return s
}

func (s *TVRemoteService) handleKeyEvent(w http.ResponseWriter, r *http.Request) {
	keycode := r.FormValue("keycode")
	if keycode == "" {
		http.Error(w, "keycode is required", http.StatusBadRequest)
		return
	}

	sendCommand := func() error {
		cmd := exec.Command("adb", "shell", "input", "keyevent", keycode)
		log.Printf("Executing ADB command: %s", cmd.String())
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("ADB command 'input keyevent %s' failed. Output: %s, Error: %v", keycode, string(output), err)
		} else if len(output) > 0 {
			log.Printf("ADB command 'input keyevent %s' output: %s", keycode, string(output))
		}
		return err
	}

	err := sendCommand()
	if err == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	log.Printf("First attempt to send key event %s failed. Error: %v. Attempting reconnect.", keycode, err)
	if s.reconnectFunc == nil {
		log.Printf("Reconnect function is not set for TVRemoteService.")
		http.Error(w, "Failed to send key event, reconnect unavailable", http.StatusInternalServerError)
		return
	}
	if reconErr := s.reconnectFunc(); reconErr != nil {
		log.Printf("Failed to reconnect to ADB: %v", reconErr)
		http.Error(w, "Failed to send key event after reconnect attempt failed", http.StatusInternalServerError)
		return
	}
	log.Printf("ADB reconnected successfully. Retrying key event %s.", keycode)
	// Retry the command
	err = sendCommand()
	if err != nil {
		log.Printf("Failed to send key event %s on second attempt: %v", keycode, err)
		http.Error(w, "Failed to send key event after retry", http.StatusInternalServerError)
		return
	}
}

func (s *TVRemoteService) handleToggleScreen(w http.ResponseWriter, r *http.Request) {
	s.scrcpyMu.Lock()
	defer s.scrcpyMu.Unlock()

	if s.scrcpyCmd != nil && s.scrcpyCmd.Process != nil {
		// If scrcpy is running, stop it by sending SIGINT
		log.Println("Stopping scrcpy...")
		if err := s.scrcpyCmd.Process.Signal(os.Interrupt); err != nil {
			log.Printf("Failed to send SIGINT to scrcpy process: %v", err)
			http.Error(w, "Failed to stop scrcpy", http.StatusInternalServerError)
			return
		}
		// Wait for the process to exit to release resources.
		if _, err := s.scrcpyCmd.Process.Wait(); err != nil {
			log.Printf("Error waiting for scrcpy process to exit: %v", err)
			// Continue, as the signal was sent and we intend to clear s.scrcpyCmd.
		}
		log.Println("scrcpy process stopped.")
		s.scrcpyCmd = nil
		w.WriteHeader(http.StatusOK)
	} else {
		// If scrcpy is not running, start it
		log.Println("Attempting to start scrcpy...")

		// Helper function to start scrcpy
		startScrcpy := func() (*exec.Cmd, error) {
			// Assuming scrcpy is in the PATH.
			// ANDROID_SERIAL and ADB_SERVER_SOCKET are set globally in main()
			// and should be picked up by scrcpy.
			c := exec.Command("scrcpy", "--no-playback", "-S")
			c.Stdout = os.Stdout // Pipe scrcpy output to service logs
			c.Stderr = os.Stderr
			err := c.Start()
			if err != nil {
				return nil, fmt.Errorf("scrcpy cmd.Start() failed: %w", err)
			}
			return c, nil
		}

		var currentScrcpyCmd *exec.Cmd
		var startErr error

		currentScrcpyCmd, startErr = startScrcpy()

		if startErr != nil {
			log.Printf("Failed to start scrcpy on first attempt: %v", startErr)
			log.Println("Attempting ADB reconnect before retrying scrcpy...")
			if s.reconnectFunc == nil {
				log.Printf("Reconnect function is not set for TVRemoteService.")
				http.Error(w, "Failed to start scrcpy, reconnect unavailable", http.StatusInternalServerError)
				return
			}
			if reconErr := s.reconnectFunc(); reconErr != nil {
				log.Printf("Failed to reconnect to ADB: %v", reconErr)
				http.Error(w, "Failed to start scrcpy after reconnect attempt failed", http.StatusInternalServerError)
				return
			}
			log.Println("ADB reconnected successfully. Retrying scrcpy.")
			currentScrcpyCmd, startErr = startScrcpy() // Retry
			if startErr != nil {
				log.Printf("Failed to start scrcpy on second attempt: %v", startErr)
				http.Error(w, "Failed to start scrcpy after retry", http.StatusInternalServerError)
				return // Crucial: ensure we don't proceed if the second attempt fails
			}
		}

		// At this point, currentScrcpyCmd.Start() has succeeded.
		log.Printf("scrcpy process started (PID: %d). Monitoring for 1 second...", currentScrcpyCmd.Process.Pid)
		s.scrcpyCmd = currentScrcpyCmd // Store the command; it will be cleared if it exits prematurely

		done := make(chan error, 1)
		go func() {
			// This goroutine waits for the command to exit.
			done <- currentScrcpyCmd.Wait()
		}()

		select {
		case <-time.After(1 * time.Second):
			// Process is still running after 1 second.
			log.Printf("scrcpy (PID: %d) confirmed running after 1 second.", currentScrcpyCmd.Process.Pid)
			w.WriteHeader(http.StatusOK) // Send success response
		case waitErr := <-done:
			// Process exited within 1 second.
			pid := currentScrcpyCmd.Process.Pid
			log.Printf("scrcpy process (PID: %d) exited prematurely within 1 second. Error from Wait: %v", pid, waitErr)
			errMsg := fmt.Sprintf("scrcpy (PID: %d) failed to stay running or exited prematurely within 1 second: %v", pid, waitErr)
			http.Error(w, errMsg, http.StatusInternalServerError)
			s.scrcpyCmd = nil // Clear the command as it's no longer running
		}
	}
}

// startAndConnectAdb starts the ADB server (if not already running on the configured port)
// and connects to the specified ADB device.
// It assumes ADB_SERVER_SOCKET is already set in the environment.
func startAndConnectAdb(host string, port int, localServerPort int) error {
	// Start ADB server
	cmdStart := exec.Command("adb", "start-server")
	log.Printf("Executing ADB command: %s (aiming for server on ADB_SERVER_SOCKET=tcp:localhost:%d)", cmdStart.String(), localServerPort)
	if output, err := cmdStart.CombinedOutput(); err != nil {
		// Log output for debugging, as start-server can sometimes print useful info
		// even on failure, or if it's already running.
		log.Printf("ADB start-server command output: %s", string(output))
		// Note: `adb start-server` might return an error even if a server is already running.
		// The crucial part is that `connect` works with the server at ADB_SERVER_SOCKET.
		// However, a genuine failure to start the server is an issue.
		return fmt.Errorf("failed to start/ensure ADB server (expected on port %d via ADB_SERVER_SOCKET): %w. Output: %s", localServerPort, err, string(output))
	}
	log.Printf("ADB server started/ensured running (expected on port %d via ADB_SERVER_SOCKET)", localServerPort)

	// Connect to ADB device
	adbAddress := host + ":" + strconv.Itoa(port)
	cmdConnect := exec.Command("adb", "connect", adbAddress)
	log.Printf("Executing ADB command: %s (using server on ADB_SERVER_SOCKET)", cmdConnect.String())
	if output, err := cmdConnect.CombinedOutput(); err != nil {
		log.Printf("ADB connect command output: %s", string(output))
		return fmt.Errorf("failed to connect to ADB device at %s (via server on port %d): %w. Output: %s", adbAddress, localServerPort, err, string(output))
	}
	log.Printf("Connected to ADB device at %s (via server on port %d)", adbAddress, localServerPort)
	return nil
}

func main() {
	socketPath := flag.String("unix", "", "Path to the Unix domain socket for the HTTP server. If set, takes precedence over -http.")
	httpAddr := flag.String("http", defaultHttpAddr, "HTTP listen address (e.g., :8080). Used if -socket is not provided.")
	adbLocalServerPort := flag.Int("adb-port", 27754, "Port for the local ADB server.")
	flag.Parse()

	// Set environment variable for ADB server socket - called only once here.
	// This affects all subsequent `adb` commands in this process.
	adbServerSocketEnv := "tcp:localhost:" + strconv.Itoa(*adbLocalServerPort)
	if err := os.Setenv("ADB_SERVER_SOCKET", adbServerSocketEnv); err != nil {
		log.Fatalf("Failed to set ADB_SERVER_SOCKET environment variable: %v", err)
	}
	log.Printf("ADB_SERVER_SOCKET set to %s", adbServerSocketEnv)

	// Set ANDROID_SERIAL to target a specific device.
	if err := os.Setenv("ANDROID_SERIAL", expectedAdbSerial); err != nil {
		log.Fatalf("Failed to set ANDROID_SERIAL environment variable: %v", err)
	}
	log.Printf("ANDROID_SERIAL set to %s", expectedAdbSerial)

	// Create the TVRemoteService instance
	tvService := NewTVRemoteService(adbHost, adbPort, *adbLocalServerPort)

	// Initial ADB setup: Start server and connect to device
	if err := tvService.reconnectFunc(); err != nil {
		log.Fatalf("Initial ADB setup failed: %v. Ensure ADB is installed and accessible.", err)
	}

	// Determine listen address (socket or HTTP)
	var listener net.Listener
	var listenErr error
	if os.Getenv("LISTEN_FDS") == "1" {
		// Systemd socket activation
		log.Println("Using systemd socket activation (fd=3)")
		file := os.NewFile(3, "systemd-socket")
		listener, listenErr = net.FileListener(file)
		if closeErr := file.Close(); closeErr != nil {
			log.Printf("Error closing file: %v", closeErr)
		}
		if listenErr != nil {
			log.Fatalf("Failed to use systemd activated socket: %v", listenErr)
		}
	} else if *socketPath != "" {
		// Unix socket specified by flag
		log.Printf("Listening on unix socket: %s", *socketPath)
		listener, listenErr = net.Listen("unix", *socketPath)
		if listenErr != nil {
			log.Fatalf("Failed to listen on unix socket %s: %v", *socketPath, listenErr)
		}
		defer os.RemoveAll(*socketPath) // Clean up socket file on exit
	} else {
		// HTTP address specified by flag
		log.Printf("Server listening on HTTP address: %s", *httpAddr)
		log.Fatal(http.ListenAndServe(*httpAddr, &tvService.serveMux))
		return // http.ListenAndServe is blocking, so we return here
	}
	defer listener.Close()
	log.Printf("Server listening on %s", listener.Addr())
	log.Fatal(http.Serve(listener, &tvService.serveMux))
}
