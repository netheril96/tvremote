package lib

import (
	"net/http"
	"sync"

	"github.com/electricbubble/gadb"
)

type TVRemoteService struct {
	adbDeviceCreator func() (*gadb.Device, error)
	mu               sync.Mutex
	device           *gadb.Device
	serveMux         http.ServeMux
}

func NewTVRemoteService(adbDeviceCreator func() (*gadb.Device, error)) *TVRemoteService {
	s := &TVRemoteService{
		adbDeviceCreator: adbDeviceCreator,
	}
	s.serveMux.HandleFunc("POST /api/keyevent", s.handleKeyEvent)
	return s
}

func (s *TVRemoteService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.serveMux.ServeHTTP(w, r)
}

func (s *TVRemoteService) getDevice() (*gadb.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.device != nil {
		return s.device, nil
	}
	dev, err := s.adbDeviceCreator()
	if err != nil {
		return nil, err
	}
	s.device = dev
	return dev, nil
}

func (s *TVRemoteService) handleKeyEvent(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	if key == "" {
		http.Error(w, "Missing 'key' parameter", http.StatusBadRequest)
		return
	}
	// Validate that key consists only of alphanumeric characters, underscores, and hyphens
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			http.Error(w, "Invalid characters in 'key' parameter. Only alphanumeric, underscore, and hyphen are allowed.", http.StatusBadRequest)
			return
		}
	}

	dev, err := s.getDevice()
	if err != nil {
		http.Error(w, "Failed to get device: "+err.Error(), http.StatusInternalServerError)
		return
	}

	cmd := "input keyevent " + key // `key` has no special characters, so it's safe to use directly
	_, err = dev.RunShellCommandWithBytes(cmd)
	if err != nil {
		http.Error(w, "Failed to send keyevent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
