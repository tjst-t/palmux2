package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/agent/proto"
)

// -------- Echo --------

func (s *Server) handleEcho(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.EchoParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	return proto.EchoResult{Msg: p.Msg, AgentVersion: proto.Version}, nil
}

// -------- ListListeningPorts --------

// handleListListeningPorts reads /proc/net/tcp and /proc/net/tcp6 to enumerate
// LISTEN-state sockets without depending on lsof, ss, or netstat.
//
// [AC-S98156b-1-4] /proc/net/tcp parser, IPv4/IPv6 both.
func (s *Server) handleListListeningPorts(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.ListListeningPortsParams
	if raw != nil {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "invalid params: " + err.Error()}
		}
	}

	ports, err := parseProcNetTCP("/proc/net/tcp", "tcp")
	if err != nil {
		return nil, fmt.Errorf("agent: parse /proc/net/tcp: %w", err)
	}

	if !p.IPv4Only {
		ports6, err := parseProcNetTCP("/proc/net/tcp6", "tcp6")
		if err == nil {
			ports = append(ports, ports6...)
		}
		// /proc/net/tcp6 may not exist if IPv6 is disabled — that's fine.
	}

	return proto.ListListeningPortsResult{Ports: ports}, nil
}

// parseProcNetTCP parses /proc/net/tcp or /proc/net/tcp6.
// Only rows with state=0x0A (LISTEN) are included.
//
// Format:
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode
//	0:  00000000:1F90 00000000:0000 0A ...
func parseProcNetTCP(path, protocol string) ([]proto.PortEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []proto.PortEntry
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // skip header
		}
		line := strings.TrimSpace(sc.Text())
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		stateHex := fields[3]
		if !strings.EqualFold(stateHex, "0A") { // LISTEN
			continue
		}
		localAddr := fields[1] // e.g. "00000000:1F90" or "00000000000000000000000001000000:1F90"
		parts := strings.SplitN(localAddr, ":", 2)
		if len(parts) != 2 {
			continue
		}
		portNum, err := strconv.ParseUint(parts[1], 16, 16)
		if err != nil {
			continue
		}

		ip := parseHexIP(parts[0])
		entries = append(entries, proto.PortEntry{
			Port:         uint16(portNum),
			Protocol:     protocol,
			LocalAddress: ip,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// parseHexIP converts a hex-encoded IP address to dotted-decimal or IPv6 notation.
// For /proc/net/tcp (IPv4), the 8-char hex is little-endian 32-bit.
// For /proc/net/tcp6 (IPv6), the 32-char hex is four 32-bit LE words.
func parseHexIP(hexIP string) string {
	switch len(hexIP) {
	case 8: // IPv4 little-endian
		n, err := strconv.ParseUint(hexIP, 16, 32)
		if err != nil {
			return hexIP
		}
		// little-endian: byte 0 is least significant
		b0 := byte(n)
		b1 := byte(n >> 8)
		b2 := byte(n >> 16)
		b3 := byte(n >> 24)
		if n == 0 {
			return "0.0.0.0"
		}
		return fmt.Sprintf("%d.%d.%d.%d", b0, b1, b2, b3)
	case 32: // IPv6 — four 32-bit LE words
		if hexIP == strings.Repeat("0", 32) {
			return "::"
		}
		var words [4]uint32
		for i := 0; i < 4; i++ {
			chunk := hexIP[i*8 : (i+1)*8]
			n, err := strconv.ParseUint(chunk, 16, 32)
			if err != nil {
				return hexIP
			}
			// Each 32-bit word is also little-endian.
			words[i] = uint32(n>>24) | uint32((n>>16)&0xff)<<8 | uint32((n>>8)&0xff)<<16 | uint32(n&0xff)<<24
		}
		// Reconstruct as 8 groups of 16-bit.
		groups := make([]string, 8)
		for i := 0; i < 4; i++ {
			groups[i*2] = fmt.Sprintf("%x", words[i]>>16)
			groups[i*2+1] = fmt.Sprintf("%x", words[i]&0xffff)
		}
		return strings.Join(groups, ":")
	}
	return hexIP
}

// -------- Shared: path sandbox validation --------

// securePath resolves p relative to root and verifies it does not escape root.
// It rejects ".." components before resolution and also verifies via EvalSymlinks.
//
// [AC-S98156b-1-5] path traversal + symlink escape prevention.
func securePath(root, p string) (string, error) {
	// Reject obviously bad paths.
	if strings.Contains(p, "..") {
		return "", &proto.RPCErr{
			Code:    proto.ErrCodeForbidden,
			Message: "path traversal rejected: '..' component",
		}
	}

	// Clean and join.
	cleaned := filepath.Join(root, filepath.Clean("/"+p))

	// EvalSymlinks to resolve any symlinks before the prefix check.
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			// For Stat/Walk the path must exist; return a clear error.
			return "", &proto.RPCErr{
				Code:    proto.ErrCodeInternal,
				Message: "path not found: " + p,
			}
		}
		return "", fmt.Errorf("agent: eval symlinks %q: %w", cleaned, err)
	}

	// Resolve root itself too.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		// If root doesn't exist, deny everything.
		return "", fmt.Errorf("agent: eval symlinks root %q: %w", root, err)
	}

	if !strings.HasPrefix(resolved+string(filepath.Separator), resolvedRoot+string(filepath.Separator)) &&
		resolved != resolvedRoot {
		return "", &proto.RPCErr{
			Code:    proto.ErrCodeForbidden,
			Message: "path escapes sandbox root",
		}
	}
	return resolved, nil
}

// -------- ReadFile --------

func (s *Server) handleReadFile(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.ReadFileParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if p.Root == "" {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "root is required"}
	}

	resolved, err := securePath(p.Root, p.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("agent: stat %q: %w", resolved, err)
	}
	if info.IsDir() {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "path is a directory"}
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("agent: read file %q: %w", resolved, err)
	}
	return proto.ReadFileResult{
		Content:  base64.StdEncoding.EncodeToString(data),
		Encoding: "base64",
		Size:     info.Size(),
	}, nil
}

// -------- Stat --------

func (s *Server) handleStat(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.StatParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if p.Root == "" {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "root is required"}
	}

	resolved, err := securePath(p.Root, p.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("agent: stat %q: %w", resolved, err)
	}
	return proto.StatResult{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		IsDir:   info.IsDir(),
		ModTime: info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

// -------- Walk --------

func (s *Server) handleWalk(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.WalkParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if p.Root == "" {
		return nil, &proto.RPCErr{Code: proto.ErrCodeInvalidParams, Message: "root is required"}
	}

	resolved, err := securePath(p.Root, p.Path)
	if err != nil {
		return nil, err
	}

	var entries []proto.WalkEntry
	err = filepath.WalkDir(resolved, func(absPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable dirs
		}
		// Depth check.
		if p.MaxDepth > 0 {
			rel, _ := filepath.Rel(resolved, absPath)
			depth := len(strings.Split(rel, string(filepath.Separator)))
			if depth > p.MaxDepth {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(resolved, absPath)
		if err != nil {
			return nil
		}
		entries = append(entries, proto.WalkEntry{
			RelPath: rel,
			Name:    d.Name(),
			IsDir:   d.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("agent: walk %q: %w", resolved, err)
	}
	return proto.WalkResult{Entries: entries}, nil
}
