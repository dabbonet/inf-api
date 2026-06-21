package codebuff

import (
	"bytes"
	"strings"
	"testing"

	"orchids-api/internal/config"
	"orchids-api/internal/store"
)

func TestProvider_BuildChunkRewriter(t *testing.T) {
	acc := &store.Account{ID: 1, Token: "x"}
	cfg := &config.Config{}
	p := NewFromAccount(acc, cfg)
	if p == nil {
		t.Fatal("provider nil with token")
	}
	fn := p.BuildChunkRewriter()
	if fn == nil {
		t.Fatal("nil rewriter")
	}
	in := []byte(`{"id":"msg_42","object":"chat.completion.chunk","model":"claude"}`)
	out := fn(in)
	if bytes.Contains(out, []byte(`"id":"msg_`)) {
		t.Fatalf("msg id not rewritten: %s", out)
	}
	if !strings.Contains(string(out), `"id":"chatcmpl-`) {
		t.Fatalf("chatcmpl id missing: %s", out)
	}
}
