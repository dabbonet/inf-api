package codebuff

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestChunkRewriter_ReplacesMsgIDWithChatID(t *testing.T) {
	cr := NewChunkRewriter()
	if !strings.HasPrefix(cr.ChatID(), "chatcmpl-") {
		t.Fatalf("ChatID=%q does not start with chatcmpl-", cr.ChatID())
	}

	raw := []byte(`{"id":"msg_1234567890","object":"chat.completion.chunk","model":"claude"}`)
	out := cr.RewriteLine(raw)

	if bytes.Contains(out, []byte(`"id":"msg_`)) {
		t.Fatalf("msg_ token still present: %s", out)
	}
	if !bytes.Contains(out, []byte(`"id":"`+cr.ChatID()+`"`)) {
		t.Fatalf("chatcmpl ID missing: %s", out)
	}
	if !bytes.Contains(out, []byte(`"object":"chat.completion.chunk"`)) {
		t.Fatalf("other fields disturbed: %s", out)
	}
}

func TestChunkRewriter_NoMsgIDUnchanged(t *testing.T) {
	cr := NewChunkRewriter()
	raw := []byte(`{"object":"chat.completion.chunk","model":"claude"}`)
	out := cr.RewriteLine(raw)
	if !bytes.Equal(out, raw) {
		t.Fatalf("changed chunk without msg_ id: %s -> %s", raw, out)
	}
}

func TestChunkRewriter_EmptyAndNilSafe(t *testing.T) {
	cr := NewChunkRewriter()
	if out := cr.RewriteLine(nil); out != nil {
		t.Fatalf("nil in -> nil out, got %v", out)
	}
	if out := cr.RewriteLine([]byte{}); len(out) != 0 {
		t.Fatalf("empty in -> empty out, got %v", out)
	}
}

func TestChunkRewriter_Idempotent(t *testing.T) {
	cr := NewChunkRewriter()
	raw := []byte(`{"id":"msg_42","object":"chat.completion.chunk"}`)
	once := cr.RewriteLine(raw)
	twice := cr.RewriteLine(once)
	if !bytes.Equal(once, twice) {
		t.Fatalf("second rewrite changed output:\n%s\n%s", once, twice)
	}
}

func TestChunkRewriter_DoesNotMatchOtherMsgReferences(t *testing.T) {
	cr := NewChunkRewriter()
	raw := []byte(`{"object":"chat.completion.chunk","choices":[{"delta":{"content":"msg_42"}}]}`)
	out := cr.RewriteLine(raw)
	if bytes.Contains(out, []byte(`"id":"`+cr.ChatID()+`"`)) {
		t.Fatalf("content 'msg_42' was mis-substituted as id: %s", out)
	}
}

func TestChatID_HexPattern(t *testing.T) {
	cr := NewChunkRewriter()
	pattern := regexp.MustCompile(`^chatcmpl-[0-9a-f]{24}$`)
	if !pattern.MatchString(cr.ChatID()) {
		t.Fatalf("ChatID=%q does not match ^chatcmpl-[0-9a-f]{24}$", cr.ChatID())
	}
}

func TestChunkRewriter_PreservesTrailingStructure(t *testing.T) {
	cr := NewChunkRewriter()
	raw := []byte(`{"id":"msg_1781981498123","object":"chat.completion.chunk","model":"claude","choices":[{}]}`)
	out := cr.RewriteLine(raw)
	if bytes.Contains(out, []byte(`""`)) {
		t.Fatalf("double-quote artifact: %s", out)
	}
	if !bytes.HasSuffix(out, []byte(`]}`)) {
		t.Fatalf("trailing structure lost: %s", out)
	}
	if !bytes.Contains(out, []byte(`"id":"`+cr.ChatID()+`"`)) {
		t.Fatalf("chatcmpl id not present: %s", out)
	}
}
