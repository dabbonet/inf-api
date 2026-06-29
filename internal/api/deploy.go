package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/redis/go-redis/v9"
)

// Internal deploy endpoint. Build + replace the running orchids-server binary
// in-process. Drops in-progress + last-completed state to Redis so observability
// survives process restarts. On success: os.Exit(0) → systemd Restart=always
// brings up the new binary.
//
// Wired at /api/internal/deploy with sessionAuth.

const deployStateKey = "orchids:internal:deploy:state"

type deployState struct {
	InFlight      bool      `json:"in_flight"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
	DurationMs    int64     `json:"duration_ms"`
	SHA256        string    `json:"sha256"`
	Size          int64     `json:"size"`
	Status        string    `json:"status"`  // "idle"|"building"|"built"|"error"
	Error         string    `json:"error"`
	RequestedBy   string    `json:"requested_by"`
	LastExitPid   int       `json:"last_exit_pid"`
	LastStartedAt time.Time `json:"last_started_at"`
}

var (
	deployMu       sync.Mutex
	deployInFlight bool
	deployRunning  *deployState // memory mirror of in-flight (so we can refuse a second POST)
)

// HandleInternalDeploy — POST kicks off build, GET returns persisted state.
func (a *API) HandleInternalDeploy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		state, err := loadDeployState(r.Context(), a)
		if err != nil {
			slog.Warn("deploy state load failed", "error", err)
		}
		if state == nil {
			state = &deployState{Status: "idle"}
		}
		// Memory overlay: if we're currently building, reflect that even before
		// the goroutine writes to Redis.
		deployMu.Lock()
		state.InFlight = deployInFlight
		if deployRunning != nil {
			state.StartedAt = deployRunning.StartedAt
			state.Status = "building"
		}
		deployMu.Unlock()
		_ = json.NewEncoder(w).Encode(state)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Refuse concurrent deploys.
	deployMu.Lock()
	if deployInFlight {
		deployMu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    "busy",
			"in_flight": true,
		})
		return
	}
	deployInFlight = true
	running := &deployState{
		StartedAt:   time.Now().UTC(),
		Status:      "building",
		RequestedBy: clientIdentity(r),
	}
	deployRunning = running
	deployMu.Unlock()

	// Persist "building" state immediately so a GET reflects it.
	_ = saveDeployState(r.Context(), a, running)

	// Kick off build in goroutine so the response can flush.
	go func() {
		sha, size, err := runInfApiBuild()
		deployMu.Lock()
		defer deployMu.Unlock()
		completed := time.Now().UTC()
		dur := completed.Sub(running.StartedAt)
		running.CompletedAt = completed
		running.DurationMs = dur.Milliseconds()
		running.LastExitPid = os.Getpid()
		running.LastStartedAt = time.Now().UTC() // when this new process started (proxy)

		if err != nil {
			running.Status = "error"
			running.Error = err.Error()
			slog.Error("internal deploy build failed", "error", err)
		} else {
			running.Status = "built"
			running.SHA256 = sha
			running.Size = size
			slog.Info("internal deploy build ok; exiting for systemd restart",
				"sha", sha[:12], "size", size, "duration", dur.Round(time.Millisecond))
		}
		_ = saveDeployState(context.Background(), a, running)

		// Reset in-flight; we set this AFTER save so a racing GET would still
		// see "building" until the new state is persisted.
		deployInFlight = false
		deployRunning = nil

		if running.Status == "built" {
			// Drain in-flight HTTP, then exit cleanly.
			time.Sleep(750 * time.Millisecond)
			os.Exit(0)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":       "started",
		"service":      "orchids-api",
		"started_at":   running.StartedAt,
		"requested_by": running.RequestedBy,
	})
}

// runInfApiBuild builds orchids-server in $HOME/dabbo-state/ops/inf-api.
func runInfApiBuild() (sha string, size int64, err error) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/ubuntu"
	}
	repo := filepath.Join(home, "dabbo-state", "ops", "inf-api")
	if _, statErr := os.Stat(filepath.Join(repo, "go.mod")); statErr != nil {
		return "", 0, fmt.Errorf("inf-api repo not found at %s: %w", repo, statErr)
	}

	binary := filepath.Join(repo, "orchids-server")
	tmp := binary + ".new"

	cmd := exec.Command("go", "build", "-trimpath", "-o", tmp, "./cmd/server/")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", 0, fmt.Errorf("go build: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	info, err := os.Stat(tmp)
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, binary); err != nil {
		return "", 0, fmt.Errorf("rename %s -> %s: %w", tmp, binary, err)
	}

	f, err := os.Open(binary)
	if err != nil {
		return "", 0, fmt.Errorf("open for hash: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, fmt.Errorf("hash: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), info.Size(), nil
}

// saveDeployState / loadDeployState — Redis-backed, TTL 90 days.
func saveDeployState(ctx context.Context, a *API, st *deployState) error {
	rc := redisFromAPI(a)
	if rc == nil {
		return nil // no redis = no persist (test mode)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return rc.Set(ctx, deployStateKey, b, 90*24*time.Hour).Err()
}

func loadDeployState(ctx context.Context, a *API) (*deployState, error) {
	rc := redisFromAPI(a)
	if rc == nil {
		return nil, nil
	}
	val, err := rc.Get(ctx, deployStateKey).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st deployState
	if err := json.Unmarshal([]byte(val), &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func redisFromAPI(a *API) *redis.Client {
	if a == nil || a.store == nil {
		return nil
	}
	if rc := a.store.RedisClient(); rc != nil {
		return rc
	}
	return nil
}

// clientIdentity picks the best human-readable identifier from request headers.
func clientIdentity(r *http.Request) string {
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	return r.RemoteAddr
}
