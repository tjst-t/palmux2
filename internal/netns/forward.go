package netns

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// slirp4netns API request/response types.
type slirpRequest struct {
	Execute   string         `json:"execute"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type slirpResponse struct {
	Return any    `json:"return,omitempty"`
	Error  *slirpError `json:"error,omitempty"`
}

type slirpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

// hostFwdBindAddr is the address slirp4netns binds the forwarded host port to.
// "0.0.0.0" makes the port reachable from the LAN (matching palmux2's
// "PC + mobile + tablet from anywhere on the network" mission). To restrict
// to localhost-only, override via the (future) network.bindAddr setting.
const hostFwdBindAddr = "0.0.0.0"

// addHostFwd calls slirp4netns' add_hostfwd API to create a port forward
// from host hostFwdBindAddr:hostPort to guest 10.0.2.100:internalPort.
func addHostFwd(socketPath string, hostPort, internalPort int) (PortMapping, error) {
	resp, err := slirpCall(socketPath, slirpRequest{
		Execute: "add_hostfwd",
		Arguments: map[string]any{
			"proto":      "tcp",
			"host_addr":  hostFwdBindAddr,
			"host_port":  hostPort,
			"guest_addr": "10.0.2.100",
			"guest_port": internalPort,
		},
	})
	if err != nil {
		return PortMapping{}, err
	}
	_ = resp
	return PortMapping{
		HostPort:     hostPort,
		InternalPort: internalPort,
		CreatedAt:    time.Now(),
	}, nil
}

// removeHostFwd calls slirp4netns' remove_hostfwd API.
func removeHostFwd(socketPath string, hostPort, internalPort int) error {
	_, err := slirpCall(socketPath, slirpRequest{
		Execute: "remove_hostfwd",
		Arguments: map[string]any{
			"proto":      "tcp",
			"host_addr":  hostFwdBindAddr,
			"host_port":  hostPort,
			"guest_addr": "10.0.2.100",
			"guest_port": internalPort,
		},
	})
	return err
}

// slirpCall sends a JSON request to the slirp4netns API socket and returns the response.
func slirpCall(socketPath string, req slirpRequest) (*slirpResponse, error) {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("forward: dial slirp socket %s: %w", socketPath, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, fmt.Errorf("forward: set deadline: %w", err)
	}

	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("forward: marshal request: %w", err)
	}
	b = append(b, '\n')

	if _, err := conn.Write(b); err != nil {
		return nil, fmt.Errorf("forward: write request: %w", err)
	}

	dec := json.NewDecoder(conn)
	var resp slirpResponse
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("forward: decode response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("forward: slirp4netns error: %s: %s", resp.Error.Class, resp.Error.Desc)
	}
	return &resp, nil
}
