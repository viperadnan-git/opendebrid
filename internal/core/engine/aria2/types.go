package aria2

import "github.com/viperadnan-git/opendebrid/internal/core/engine"

// aria2 JSON-RPC request/response types

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      string    `json:"id"`
	Result  any       `json:"result"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type statusResponse struct {
	GID             string      `json:"gid"`
	Status          string      `json:"status"`
	TotalLength     string      `json:"totalLength"`
	CompletedLength string      `json:"completedLength"`
	DownloadSpeed   string      `json:"downloadSpeed"`
	ErrorCode       string      `json:"errorCode"`
	ErrorMessage    string      `json:"errorMessage"`
	Dir             string      `json:"dir"`
	InfoHash        string      `json:"infoHash"`
	NumSeeders      string      `json:"numSeeders"`
	Connections     string      `json:"connections"`
	Seeder          string      `json:"seeder"`
	BitTorrent      *btInfo     `json:"bittorrent"`
	Files           []fileEntry `json:"files"`
	FollowedBy      []string    `json:"followedBy"`
	Following       string      `json:"following"`
}

type btInfo struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
}

type fileEntry struct {
	Index           string `json:"index"`
	Path            string `json:"path"`
	Length          string `json:"length"`
	CompletedLength string `json:"completedLength"`
	Selected        string `json:"selected"`
}

// mapStatus maps aria2 status to core engine states.
// seeder indicates if the torrent is in seeding mode (download finished).
func mapStatus(aria2Status string, seeder bool) (engine.JobState, string) {
	switch aria2Status {
	case "active":
		if seeder {
			return engine.StateCompleted, "seeding"
		}
		return engine.StateActive, "downloading"
	case "waiting":
		return engine.StateQueued, "waiting"
	case "paused":
		return engine.StateActive, "paused"
	case "complete":
		return engine.StateCompleted, "complete"
	case "removed":
		return engine.StateCancelled, "removed"
	case "error":
		return engine.StateFailed, "error"
	default:
		return engine.StateActive, aria2Status
	}
}
