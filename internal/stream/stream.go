package stream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

type StreamHook func(chunk []byte) ([]byte, error)

type Pipeline struct {
	hooks []StreamHook
}

func NewPipeline(hooks ...StreamHook) *Pipeline {
	return &Pipeline{hooks: hooks}
}

func (p *Pipeline) Process(r io.Reader, w http.ResponseWriter, flusher http.Flusher) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		chunk := scanner.Bytes()
		if len(chunk) == 0 {
			continue
		}

		for _, hook := range p.hooks {
			var err error
			chunk, err = hook(chunk)
			if err != nil {
				return err
			}
			if len(chunk) == 0 {
				break
			}
		}

		if len(chunk) == 0 {
			continue
		}

		if _, err := w.Write(append(chunk, '\n')); err != nil {
			return err
		}
		flusher.Flush()
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("stream scanner error", "error", err)
	}

	return nil
}

func ProcessRaw(r io.Reader, w http.ResponseWriter) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func SuppressTrailingStopEvents() StreamHook {
	return func(chunk []byte) ([]byte, error) {
		chunk = bytes.TrimSpace(chunk)
		if len(chunk) == 0 {
			return nil, nil
		}
		if !bytes.HasPrefix(chunk, []byte("data: ")) {
			return chunk, nil
		}
		data := bytes.TrimPrefix(chunk, []byte("data: "))
		if bytes.Equal(data, []byte("[DONE]")) {
			return nil, nil
		}
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &event); err == nil && event.Type == "message_stop" {
			return nil, nil
		}
		return chunk, nil
	}
}
