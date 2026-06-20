package codebuff

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"sync"
)

var openAIMsgIDMarker = []byte(`"id":"msg_`)

type ChunkRewriter struct {
	mu        sync.Mutex
	chatID    string
	chatIDRaw []byte
}

func NewChunkRewriter() *ChunkRewriter {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		for i := range b {
			b[i] = byte(i)
		}
	}
	id := hex.EncodeToString(b[:])
	chatID := "chatcmpl-" + id
	return &ChunkRewriter{
		chatID:    chatID,
		chatIDRaw: []byte(`"` + chatID + `"`),
	}
}

func (cr *ChunkRewriter) ChatID() string {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return cr.chatID
}

func (cr *ChunkRewriter) RewriteLine(raw []byte) []byte {
	if len(raw) == 0 || bytes.Index(raw, openAIMsgIDMarker) < 0 {
		return raw
	}
	cr.mu.Lock()
	chatID := cr.chatID
	cr.mu.Unlock()

	idReplace := []byte(`"id":"` + chatID + `"`)
	out := raw
	for {
		idx := bytes.Index(out, openAIMsgIDMarker)
		if idx < 0 {
			break
		}
		valStart := idx + len("\"id\":") + 1
		q := bytes.IndexByte(out[valStart:], '"')
		if q < 0 {
			break
		}
		valEnd := valStart + q
		newBuf := make([]byte, 0, len(out)+(len(idReplace)-(valEnd+1-idx)))
		newBuf = append(newBuf, out[:idx]...)
		newBuf = append(newBuf, idReplace...)
		newBuf = append(newBuf, out[valEnd+1:]...)
		out = newBuf
	}
	return out
}
