package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/electricbubble/gadb"
	"github.com/netheril96/tvremote/api/lib"
)

const (
	defaultHttpAddr   = "127.0.0.1:61121"
	adbHost           = "tv.lan"
	adbPort           = 5555
	expectedAdbSerial = "tv.lan:5555"
)

func main() {
	socketPath := flag.String("unix", "", "Path to the Unix domain socket for the HTTP server. If set, takes precedence over -http.")
	httpAddr := flag.String("http", defaultHttpAddr, "HTTP listen address (e.g., :8080). Used if -socket is not provided.")
	flag.Parse()

	adbDeviceCreator := func() (*gadb.Device, error) {
		client, err := gadb.NewClient()
		if err != nil {
			return nil, fmt.Errorf("failed to create adb client: %w", err)
		}

		err = client.Connect(adbHost, adbPort)
		if err != nil {
			// This error might mean the adb server itself is not reachable, or the connect command failed.
			// Depending on gadb's behavior, the device might still be listed if previously connected.
			// Propagating the error is safer.
			return nil, fmt.Errorf("adb connect to %s:%d failed: %w", adbHost, adbPort, err)
		}

		devices, err := client.DeviceList()
		if err != nil {
			return nil, fmt.Errorf("failed to list adb devices: %w", err)
		}

		for _, dev := range devices {
			if dev.Serial() == expectedAdbSerial {
				// Optionally, you could check dev.State() here, e.g.:
				// if dev.State() != gadb.StateOnline {
				// 	return nil, fmt.Errorf("device %s is not online, state: %s", expectedAdbSerial, dev.State())
				// }
				return &dev, nil
			}
		}
		return nil, fmt.Errorf("adb device %s not found in device list", expectedAdbSerial)
	}

	service := lib.NewTVRemoteService(adbDeviceCreator)

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
