package telegram

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

func tokenPayload(owner game.UserID, action string) TokenPayload {
	return TokenPayload{
		Owner:         owner,
		Action:        action,
		Target:        "3",
		ExpectedPhase: game.Phase(1),
		PhaseVersion:  7,
	}
}

func TestCallbackTokenShortBase64URL(t *testing.T) {
	m := NewCallbackManager(16)
	tok, err := m.Issue(tokenPayload(1001, "vote"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 16 字节熵 → RawURLEncoding 22 字符、无 padding。
	if len(tok) != 22 {
		t.Fatalf("token length = %d, want 22（短 base64url）", len(tok))
	}
	if strings.Contains(tok, "=") {
		t.Fatalf("token %q 含 padding，要求无 padding base64url", tok)
	}
	for _, c := range tok {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			t.Fatalf("token %q 含非法字符 %q", tok, c)
		}
	}
}

func TestCallbackTokenDoesNotLeakPayload(t *testing.T) {
	m := NewCallbackManager(16)
	tok, err := m.Issue(tokenPayload(1001, "wolf_kill"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 回调数据只暴露 token 值：不包含身份、角色或目标。
	// Target "3" 是单个字符，随机 base64url 可能偶现，不列入泄漏检查；
	// 不透明性由 token 为随机值（不编码 payload）保证，检查多字符标识。
	for _, leak := range []string{"1001", "1001:", "wolf_kill", "phase", "version", "save", "poison"} {
		if strings.Contains(tok, leak) {
			t.Fatalf("token %q 泄漏 payload 片段 %q（回调数据不得暴露身份/角色/目标）", tok, leak)
		}
	}
}

func TestCallbackValidateReturnsFullPayload(t *testing.T) {
	m := NewCallbackManager(16)
	tok, err := m.Issue(tokenPayload(1001, "vote"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, err := m.Validate(tok, 1001)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Owner != 1001 || got.Action != "vote" || got.Target != "3" ||
		got.ExpectedPhase != game.Phase(1) || got.PhaseVersion != 7 {
		t.Fatalf("payload = %+v, want owner 1001 action vote target 3 phase 1 version 7", got)
	}
}

func TestCallbackWrongOwnerRejected(t *testing.T) {
	m := NewCallbackManager(16)
	tok, _ := m.Issue(tokenPayload(1001, "vote"))
	_, err := m.Validate(tok, 2002)
	if !errors.Is(err, ErrTokenOwnerMismatch) {
		t.Fatalf("err = %v, want ErrTokenOwnerMismatch（越权回调必须拒绝）", err)
	}
}

func TestCallbackUnknownTokenRejected(t *testing.T) {
	m := NewCallbackManager(16)
	_, err := m.Validate("AAAAAAAAAAAAAAAAAAAAAA", 1001)
	if !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("err = %v, want ErrTokenNotFound", err)
	}
}

func TestCallbackInvalidatePhase(t *testing.T) {
	m := NewCallbackManager(16)
	tok1, _ := m.Issue(tokenPayload(1001, "vote"))      // phase 1
	tok2, _ := m.Issue(tokenPayload(1001, "wolf_kill")) // phase 1
	tok3, _ := m.Issue(TokenPayload{Owner: 1001, Action: "vote", Target: "1", ExpectedPhase: game.Phase(2), PhaseVersion: 1})

	m.InvalidatePhase(game.Phase(1))
	if _, err := m.Validate(tok1, 1001); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("phase 1 token 失效后 Validate err = %v, want ErrTokenNotFound", err)
	}
	if _, err := m.Validate(tok2, 1001); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("phase 1 token（第二枚）失效后 Validate err = %v, want ErrTokenNotFound", err)
	}
	if _, err := m.Validate(tok3, 1001); err != nil {
		t.Fatalf("phase 2 token 不应受失效影响: %v", err)
	}
}

func TestCallbackCapacityEviction(t *testing.T) {
	m := NewCallbackManager(2)
	first, _ := m.Issue(tokenPayload(1001, "vote"))
	_, _ = m.Issue(tokenPayload(1001, "wolf_kill"))
	third, _ := m.Issue(tokenPayload(1001, "seer_check")) // 淘汰最旧 first
	if _, err := m.Validate(first, 1001); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("被淘汰 token Validate err = %v, want ErrTokenNotFound（容量上限回收）", err)
	}
	if _, err := m.Validate(third, 1001); err != nil {
		t.Fatalf("容量内 token Validate: %v", err)
	}
}

func TestCallbackConcurrentIssueValidate(t *testing.T) {
	m := NewCallbackManager(256)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := m.Issue(tokenPayload(1001, "vote"))
			if err != nil {
				t.Errorf("Issue: %v", err)
				return
			}
			if _, err := m.Validate(tok, 1001); err != nil {
				t.Errorf("Validate(own): %v", err)
			}
			if _, err := m.Validate(tok, 9999); !errors.Is(err, ErrTokenOwnerMismatch) {
				t.Errorf("Validate(foreign) err = %v, want ErrTokenOwnerMismatch", err)
			}
		}()
	}
	wg.Wait()
}
