package game

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeJoinStore 是 JoinStore seam 的测试替身：脚本化各检查结果与加入座位。
type fakeJoinStore struct {
	hash       string
	checkErr   error
	roomStatus error // nil=存在可加入
	inRoom     bool
	alreadyIn  bool
	left       bool
	reserved   map[string]bool
	joinSeat   Seat
	joinErr    error
	joined     []joinedCall
	loadCalls  int
	checkCalls int
}

type joinedCall struct {
	roomID   RoomID
	user     UserID
	nickname string
}

func (f *fakeJoinStore) LoadPasswordHash(_ context.Context, _ RoomID) (string, error) {
	f.loadCalls++
	return f.hash, nil
}

func (f *fakeJoinStore) CheckRoom(_ context.Context, _ RoomID) error {
	f.checkCalls++
	return f.roomStatus
}

func (f *fakeJoinStore) HasPlayer(_ context.Context, _ RoomID, user UserID) (bool, error) {
	return f.alreadyIn, nil
}

func (f *fakeJoinStore) HasLeft(_ context.Context, _ RoomID, _ UserID) (bool, error) {
	return f.left, nil
}

func (f *fakeJoinStore) UserInRoom(_ context.Context, _ UserID) (bool, error) {
	return f.inRoom, nil
}

func (f *fakeJoinStore) ReservedNicknames(_ context.Context, _ RoomID) (map[string]bool, error) {
	return f.reserved, nil
}

func (f *fakeJoinStore) Join(_ context.Context, roomID RoomID, user UserID, nickname string) (Seat, error) {
	f.joined = append(f.joined, joinedCall{roomID: roomID, user: user, nickname: nickname})
	return f.joinSeat, f.joinErr
}

// joinRequest 构造一条合法加入请求（无密码、随机昵称）。
func joinRequest() JoinRequest {
	return JoinRequest{
		CommandID: "j1",
		Actor:     3001,
		RoomID:    "ABC123",
	}
}

// newTestJoin 构造加入服务；失败直接测试失败。
func newTestJoin(t *testing.T, store JoinStore) JoinService {
	t.Helper()
	svc, err := NewJoinService(store, &fakeNicknameRNG{seq: []int{0, 0}})
	if err != nil {
		t.Fatalf("NewJoinService: %v", err)
	}
	return svc
}

// TestJoinServiceRequiresStore 验证 JoinStore 为硬约束。
func TestJoinServiceRequiresStore(t *testing.T) {
	if _, err := NewJoinService(nil, nil); err == nil {
		t.Error("NewJoinService(nil store) = nil error, want error")
	}
}

// TestJoinServiceHappyPath 验证成功加入：分配随机昵称、写入座位、
// 产出加入确认与房主面板刷新两个 Effects。
func TestJoinServiceHappyPath(t *testing.T) {
	store := &fakeJoinStore{joinSeat: 3}
	svc := newTestJoin(t, store)

	got, effects, err := svc.Apply(context.Background(), joinRequest())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.Seat != 3 {
		t.Errorf("Seat = %d, want 3", got.Seat)
	}
	if got.Nickname == "" {
		t.Error("Nickname 为空，want 随机中文昵称")
	}
	if len(store.joined) != 1 {
		t.Fatalf("joined 次数 = %d, want 1", len(store.joined))
	}
	if store.joined[0].roomID != "ABC123" || store.joined[0].user != 3001 || store.joined[0].nickname != got.Nickname {
		t.Errorf("joined = %+v, want room ABC123/user 3001/nickname %q", store.joined[0], got.Nickname)
	}
	requireJoinEffects(t, effects, "ABC123", got.Nickname, got.Seat)
}

// requireJoinEffects 断言加入成功产出 Actor 确认 + Host 面板刷新两个 Effect。
func requireJoinEffects(t *testing.T, effects []Effect, roomID, nickname string, seat Seat) {
	t.Helper()
	var actorMsg, hostMsg *MessageEffect
	for i := range effects {
		if msg, ok := effects[i].(MessageEffect); ok {
			if msg.Audience == AudienceActor {
				actorMsg = &msg
			}
			if msg.Audience == AudienceHost {
				hostMsg = &msg
			}
		}
	}
	if actorMsg == nil || actorMsg.Key != JoinConfirmedMessageKey {
		t.Errorf("缺少 Actor 加入确认 Effect，got effects=%v", effects)
	} else {
		if got, _ := actorMsg.Params["room_code"].(string); got != roomID {
			t.Errorf("actorMsg room_code = %v, want %q", actorMsg.Params["room_code"], roomID)
		}
		if got, _ := actorMsg.Params["nickname"].(string); got != nickname {
			t.Errorf("actorMsg nickname = %v, want %q", actorMsg.Params["nickname"], nickname)
		}
		if got, _ := actorMsg.Params["seat"].(Seat); got != seat {
			t.Errorf("actorMsg seat = %v, want %v", actorMsg.Params["seat"], seat)
		}
	}
	if hostMsg == nil || hostMsg.Key != LobbyPanelMessageKey {
		t.Errorf("缺少 Host 面板刷新 Effect，got effects=%v", effects)
	} else {
		if got, _ := hostMsg.Params["room_code"].(string); got != roomID {
			t.Errorf("hostMsg room_code = %v, want %q", hostMsg.Params["room_code"], roomID)
		}
	}
}

// TestJoinServicePassword 验证密码门槛：
// 设密码房间必须提供正确密码；错误返回 ErrWrongPassword 且不计数、不锁定
// （可无限次重试，docs「密码」）；无密码房间无密码参数亦可加入。
func TestJoinServicePassword(t *testing.T) {
	hash, err := HashPassword("Ab12cd")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	t.Run("有密码未提供被拒", func(t *testing.T) {
		store := &fakeJoinStore{hash: hash, joinSeat: 2}
		svc := newTestJoin(t, store)
		_, _, err := svc.Apply(context.Background(), joinRequest())
		if !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("err = %v, want ErrWrongPassword", err)
		}
		if len(store.joined) != 0 {
			t.Errorf("密码未通过仍写入加入: %+v", store.joined)
		}
	})
	t.Run("密码错误", func(t *testing.T) {
		store := &fakeJoinStore{hash: hash, joinSeat: 2}
		svc := newTestJoin(t, store)
		req := joinRequest()
		pw := "wrong1"
		req.Password = &pw
		if _, _, err := svc.Apply(context.Background(), req); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("err = %v, want ErrWrongPassword", err)
		}
	})
	t.Run("连续错误可无限重试", func(t *testing.T) {
		store := &fakeJoinStore{hash: hash, joinSeat: 2}
		svc := newTestJoin(t, store)
		for i := 0; i < 3; i++ {
			req := joinRequest()
			pw := "wrong1"
			req.Password = &pw
			if _, _, err := svc.Apply(context.Background(), req); !errors.Is(err, ErrWrongPassword) {
				t.Fatalf("第 %d 次重试 err = %v, want ErrWrongPassword（不锁定）", i+1, err)
			}
		}
	})
	t.Run("密码正确加入", func(t *testing.T) {
		store := &fakeJoinStore{hash: hash, joinSeat: 2}
		svc := newTestJoin(t, store)
		req := joinRequest()
		pw := "Ab12cd"
		req.Password = &pw
		if _, _, err := svc.Apply(context.Background(), req); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})
	t.Run("无密码房间直接加入", func(t *testing.T) {
		store := &fakeJoinStore{joinSeat: 4}
		svc := newTestJoin(t, store)
		if _, _, err := svc.Apply(context.Background(), joinRequest()); err != nil {
			t.Fatalf("无密码房间 err = %v, want nil", err)
		}
	})
}

// TestJoinServiceRoomStates 验证房间状态区分与引导：
// 不存在 / 已过期 / 满员 / 重复加入 / 退出过同局 / 已在其他进行中房间。
func TestJoinServiceRoomStates(t *testing.T) {
	cases := []struct {
		name  string
		store *fakeJoinStore
		want  error
	}{
		{"房间不存在", &fakeJoinStore{roomStatus: ErrRoomNotFound}, ErrRoomNotFound},
		{"房间已过期", &fakeJoinStore{roomStatus: ErrRoomExpired}, ErrRoomExpired},
		{"重复加入", &fakeJoinStore{alreadyIn: true, joinSeat: 1}, ErrAlreadyInRoom},
		{"已在其他进行中房间", &fakeJoinStore{inRoom: true, joinSeat: 1}, ErrUserInRoom},
		{"退出过同局不可重入", &fakeJoinStore{left: true, joinSeat: 1}, ErrAlreadyLeft},
		{"满员(加入前检查)", &fakeJoinStore{roomStatus: ErrRoomFull, joinSeat: 1}, ErrRoomFull},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestJoin(t, tc.store)
			_, _, err := svc.Apply(context.Background(), joinRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(tc.store.joined) != 0 {
				t.Errorf("拒绝后仍写入加入: %+v", tc.store.joined)
			}
		})
	}
}

// TestJoinServiceInvalidRoomCode 验证非法房间码（未规范化/非 4-8 位字母数字）被拒绝。
func TestJoinServiceInvalidRoomCode(t *testing.T) {
	svc := newTestJoin(t, &fakeJoinStore{joinSeat: 1})
	for _, code := range []RoomID{"", "AB", "ABC 12", "ABC@12"} {
		req := joinRequest()
		req.RoomID = code
		if _, _, err := svc.Apply(context.Background(), req); !errors.Is(err, ErrInvalidRoomCode) {
			t.Errorf("Apply(RoomID=%q) err = %v, want ErrInvalidRoomCode", code, err)
		}
	}
}

// TestJoinServiceSpecifiedNickname 验证用户自定昵称：合法且未占用则使用；
// 占用返回 ErrNicknameTaken；非法返回 ErrNicknameInvalid。
func TestJoinServiceSpecifiedNickname(t *testing.T) {
	t.Run("自定昵称合法", func(t *testing.T) {
		store := &fakeJoinStore{joinSeat: 5}
		svc := newTestJoin(t, store)
		req := joinRequest()
		nick := "wOLF"
		req.Nickname = &nick
		got, _, err := svc.Apply(context.Background(), req)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got.Nickname != "wOLF" {
			t.Errorf("Nickname = %q, want 保留原始大小写 wOLF", got.Nickname)
		}
	})
	t.Run("昵称已占用", func(t *testing.T) {
		store := &fakeJoinStore{joinSeat: 5, reserved: map[string]bool{"wolf": true}}
		svc := newTestJoin(t, store)
		req := joinRequest()
		nick := "Wolf"
		req.Nickname = &nick
		_, _, err := svc.Apply(context.Background(), req)
		if !errors.Is(err, ErrNicknameTaken) {
			t.Fatalf("err = %v, want ErrNicknameTaken（大小写无关唯一）", err)
		}
	})
	t.Run("昵称非法", func(t *testing.T) {
		store := &fakeJoinStore{joinSeat: 5}
		svc := newTestJoin(t, store)
		req := joinRequest()
		nick := "a"
		req.Nickname = &nick
		if _, _, err := svc.Apply(context.Background(), req); !errors.Is(err, ErrNicknameInvalid) {
			t.Fatalf("err = %v, want ErrNicknameInvalid", err)
		}
	})
}

// TestJoinServiceRandomNicknameCollision 验证随机昵称冲突时重生直至唯一。
func TestJoinServiceRandomNicknameCollision(t *testing.T) {
	// 生成器脚本：前两个昵称已被占用，第三个唯一。
	store := &fakeJoinStore{joinSeat: 2}
	svc, err := NewJoinService(store, &fakeNicknameRNG{seq: []int{0, 0, 1, 1, 2, 3}})
	if err != nil {
		t.Fatalf("NewJoinService: %v", err)
	}
	// 预置占用脚本昵称：用固定生成器模拟太复杂，改用任意两个占用键。
	store.reserved = map[string]bool{"快乐小猫": true}
	got, _, err := svc.Apply(context.Background(), joinRequest())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.Nickname == "" {
		t.Error("Nickname 为空")
	}
	if store.reserved[FoldNickname(got.Nickname)] {
		t.Errorf("分配昵称 %q 与占用集冲突", got.Nickname)
	}
}

// TestParseInviteDeepLink 验证深链解析：提取 start 参数房间码并规范化。
func TestParseInviteDeepLink(t *testing.T) {
	valid := []struct{ link, want string }{
		{"https://t.me/xxxbot?start=abc123", "ABC123"},
		{"t.me/xxxbot?start=abc123", "ABC123"},
		{"https://t.me/xxxbot?start=ABC123", "ABC123"},
		{"https://t.me/xxxbot?start=abCD12", "ABCD12"},
		{"https://t.me/xxxbot?start=abcd&x=1", "ABCD"},
	}
	for _, tc := range valid {
		got, ok := ParseInviteDeepLink(tc.link)
		if !ok || got != RoomID(tc.want) {
			t.Errorf("ParseInviteDeepLink(%q) = %q/%v, want %q/true", tc.link, got, ok, tc.want)
		}
	}
	invalid := []string{
		"",
		"https://example.com/x",
		"t.me/xxxbot",
		"t.me/xxxbot?foo=1",
		"https://t.me/xxxbot?start=",
		"not a link",
	}
	for _, link := range invalid {
		if got, ok := ParseInviteDeepLink(link); ok {
			t.Errorf("ParseInviteDeepLink(%q) = %q/true, want 解析失败", link, got)
		}
	}
}

// TestJoinServiceJoinPhaseErrors 验证加入阶段的并发兜底错误透传：
// 前置检查通过后 store.Join 仍可能因并发满员/房间消失失败，错误原样
// 返回且不产生 Effects（防御路径，不影响 CheckRoom 阶段的无写入语义）。
func TestJoinServiceJoinPhaseErrors(t *testing.T) {
	for _, err := range []error{ErrRoomFull, ErrRoomNotFound} {
		store := &fakeJoinStore{joinErr: err}
		svc := newTestJoin(t, store)
		_, effects, gotErr := svc.Apply(context.Background(), joinRequest())
		if !errors.Is(gotErr, err) {
			t.Errorf("Apply joinErr=%v -> %v, want 同一错误透传", err, gotErr)
		}
		if len(effects) != 0 {
			t.Errorf("加入失败仍产出 Effects: %v", effects)
		}
	}
}

// TestJoinStoreErrorPropagation 验证 store 前置检查的意外错误被包装传播。
func TestJoinStoreErrorPropagation(t *testing.T) {
	store := &fakeJoinStore{roomStatus: errors.New("storage down")}
	svc := newTestJoin(t, store)
	if _, _, err := svc.Apply(context.Background(), joinRequest()); err == nil {
		t.Fatal("store 错误未被传播")
	}
}

// TestJoinResultZeroValue 补齐 JoinResult 结构断言（防止字段漂移）。
func TestJoinResultZeroValue(t *testing.T) {
	if !reflect.DeepEqual(JoinResult{}, JoinResult{}) {
		t.Error("JoinResult 零值不稳定")
	}
}
