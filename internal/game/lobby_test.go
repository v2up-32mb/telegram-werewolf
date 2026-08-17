package game

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedRNG 返回固定序列的随机索引（与 room 包测试同风格，
// 用于确定性验证去混淆随机码）。
type scriptedRNG struct {
	values []int
	pos    int
}

func (r *scriptedRNG) Intn(n int) (int, error) {
	if r.pos >= len(r.values) {
		return 0, errors.New("scripted RNG exhausted")
	}
	v := r.values[r.pos]
	r.pos++
	return v % n, nil
}

// panicRNG 一旦被调用即 panic：用于证明自定义码被占用时
// 绝不悄悄回退到随机码（docs「房间码规范」3）。
type panicRNG struct{}

func (panicRNG) Intn(int) (int, error) {
	panic("game: random code must not be generated when custom code is taken")
}

// fakeLobbyRegistry 是建房唯一性 seam（LobbyRoomRegistry）的测试替身。
type fakeLobbyRegistry struct {
	active  bool
	taken   map[RoomID]bool
	reserve func(ctx context.Context, code RoomID) (bool, error)
}

func (f *fakeLobbyRegistry) HostActive(UserID) bool { return f.active }

func (f *fakeLobbyRegistry) ReserveCode(ctx context.Context, code RoomID) (bool, error) {
	if f.reserve != nil {
		return f.reserve(ctx, code)
	}
	if f.taken[code] {
		return false, nil
	}
	return true, nil
}

// lobbyRequest 构造默认建房请求（空配置 = 默认 6 人局）。
func lobbyRequest() CreateRoomRequest {
	return CreateRoomRequest{CommandID: "cmd-1", Host: 1001}
}

// newTestLobby 构造测试用 LobbyService；构造失败直接失败测试。
func newTestLobby(t *testing.T, reg LobbyRoomRegistry, rng RNG) LobbyService {
	t.Helper()
	svc, err := NewLobbyService(reg, rng)
	if err != nil {
		t.Fatalf("NewLobbyService error = %v, want nil", err)
	}
	return svc
}

// requireCreateRoomEffects 断言建房成功只产生最小活跃局记录副作用。
// 创建确认文案由命令面 commands.newgame_done 承担（Task 46 冒烟修复：
// 领域层不再产出 lobby.created 文案 effect，避免缺失文案的 und 渲染
// 错误与同一次 /newgame 连发多条）。
func requireCreateRoomEffects(t *testing.T, effects []Effect, code RoomID) {
	t.Helper()
	var persist *PersistEffect
	for _, e := range effects {
		switch v := e.(type) {
		case MessageEffect:
			t.Errorf("CreateRoom 不应产出文案 effect（创建确认由命令面承担），got %T %+v", e, e)
		case PersistEffect:
			persist = &v
		}
	}
	if persist == nil {
		t.Fatal("effects 缺少 PersistEffect（最小活跃局记录）")
	}
	if persist.Kind != PersistActiveGame {
		t.Errorf("PersistEffect.Kind = %v, want PersistActiveGame", persist.Kind)
	}
}

// assertDeconfusedCode 断言随机码为 6 位且全部来自去混淆字符集。
func assertDeconfusedCode(t *testing.T, code string) {
	t.Helper()
	if len(code) != RandomRoomCodeLength {
		t.Fatalf("随机码长度 = %d, want %d (%q)", len(code), RandomRoomCodeLength, code)
	}
	for i := 0; i < len(code); i++ {
		if strings.IndexByte(lobbyRoomCodeAlphabet, code[i]) < 0 {
			t.Errorf("随机码 %q 第 %d 位 %q 不在去混淆字符集", code, i+1, code[i])
		}
	}
	for _, bad := range "01IO" {
		if strings.ContainsRune(code, bad) {
			t.Errorf("随机码 %q 含易混淆字符 %q", code, bad)
		}
	}
}

// TestDefaultCreateRoomConfig 验证 MVP 默认建房配置：6 人 =
// 2 狼 + 预言家 + 女巫 + 2 平民，默认屠城、无 AI。
func TestDefaultCreateRoomConfig(t *testing.T) {
	cfg := DefaultCreateRoomConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultCreateRoomConfig().Validate() error = %v, want nil", err)
	}
	if cfg.PlayerCount != MVPPlayerCount {
		t.Errorf("PlayerCount = %d, want %d", cfg.PlayerCount, MVPPlayerCount)
	}
	if cfg.UseAI {
		t.Error("默认配置 UseAI = true, want false")
	}
	if cfg.Victory != VictorySlaughter {
		t.Errorf("Victory = %v, want VictorySlaughter（6 人局默认屠城）", cfg.Victory)
	}
	counts := map[Role]int{}
	for _, r := range cfg.Roles {
		counts[r]++
	}
	if counts[RoleWolf] != MVPWolfCount {
		t.Errorf("狼人数 = %d, want %d", counts[RoleWolf], MVPWolfCount)
	}
	if counts[RoleSeer] != MVPSeerCount {
		t.Errorf("预言家数 = %d, want %d", counts[RoleSeer], MVPSeerCount)
	}
	if counts[RoleWitch] != MVPWitchCount {
		t.Errorf("女巫数 = %d, want %d", counts[RoleWitch], MVPWitchCount)
	}
	if counts[RoleVillager] != MVPVillagerCount {
		t.Errorf("民数 = %d, want %d", counts[RoleVillager], MVPVillagerCount)
	}
}

// TestCreateRoomDefaultsAndHostSeatOne 验证默认请求创建 6 人房间：
// 房主自动占 1 席且座位固定为 1。
func TestCreateRoomDefaultsAndHostSeatOne(t *testing.T) {
	svc := newTestLobby(t, &fakeLobbyRegistry{}, &scriptedRNG{values: []int{0, 0, 0, 0, 0, 0}})
	st, effects, err := svc.CreateRoom(context.Background(), lobbyRequest())
	if err != nil {
		t.Fatalf("CreateRoom error = %v, want nil", err)
	}
	if st.RoomID == "" {
		t.Fatal("RoomID 为空")
	}
	if st.Phase != PhaseLobby || st.PhaseVersion != 1 {
		t.Errorf("Phase/PhaseVersion = %v/%d, want PhaseLobby/1", st.Phase, st.PhaseVersion)
	}
	if len(st.Players) != 1 {
		t.Fatalf("Players 数量 = %d, want 1（仅房主）", len(st.Players))
	}
	p := st.Players[0]
	if p.UserID != 1001 || p.Seat != HostSeat {
		t.Errorf("房主玩家 = %+v, want UserID=1001 Seat=HostSeat(1)", p)
	}
	if p.Role != RoleUnknown || p.Dead {
		t.Errorf("大厅内玩家应无身份且存活，got %+v", p)
	}
	if st.Lobby.Owner != 1001 {
		t.Errorf("Lobby.Owner = %d, want 1001", st.Lobby.Owner)
	}
	if err := st.Lobby.Config.Validate(); err != nil {
		t.Errorf("Lobby.Config 非法: %v", err)
	}
	if !st.Processed["cmd-1"] {
		t.Error("Processed 未记录命令 ID，幂等键缺失")
	}
	requireCreateRoomEffects(t, effects, st.RoomID)
}

// TestCreateRoomDuplicateHostRejected 验证同一房主重复建房被明确拒绝
// （docs §一.7：房主同一时间只能开 1 个进行中的房间）。
func TestCreateRoomDuplicateHostRejected(t *testing.T) {
	svc := newTestLobby(t, &fakeLobbyRegistry{active: true}, panicRNG{})
	st, effects, err := svc.CreateRoom(context.Background(), lobbyRequest())
	if !errors.Is(err, ErrHostInRoom) {
		t.Fatalf("CreateRoom error = %v, want ErrHostInRoom", err)
	}
	if st.RoomID != "" || len(st.Players) != 0 {
		t.Errorf("拒绝后产生了部分新状态: %+v", st)
	}
	if len(effects) != 0 {
		t.Errorf("拒绝后仍产生 Effects: %v", effects)
	}
}

// TestCreateRoomRandomCodeDeconfused 验证随机码来自可注入 RNG、6 位、
// 字符集去掉 0/O、1/I。
func TestCreateRoomRandomCodeDeconfused(t *testing.T) {
	svc := newTestLobby(t, &fakeLobbyRegistry{}, &scriptedRNG{values: []int{0, 1, 2, 3, 4, 5}})
	st, _, err := svc.CreateRoom(context.Background(), lobbyRequest())
	if err != nil {
		t.Fatalf("CreateRoom error = %v, want nil", err)
	}
	if string(st.RoomID) != "ABCDEF" {
		t.Errorf("房间码 = %q, want %q（脚本随机源 0..5）", st.RoomID, "ABCDEF")
	}
	assertDeconfusedCode(t, string(st.RoomID))
}

// TestCreateRoomRandomCodeCollisionRetries 验证随机码与活跃房间碰撞时
// 重试生成，最终仍唯一。
func TestCreateRoomRandomCodeCollisionRetries(t *testing.T) {
	reg := &fakeLobbyRegistry{taken: map[RoomID]bool{"ABCDEF": true}}
	svc := newTestLobby(t, reg, &scriptedRNG{values: []int{
		0, 1, 2, 3, 4, 5, // 第一次生成 ABCDEF（碰撞）
		5, 5, 5, 5, 5, 5, // 第二次生成 FFFFFF
	}})
	st, _, err := svc.CreateRoom(context.Background(), lobbyRequest())
	if err != nil {
		t.Fatalf("CreateRoom error = %v, want nil", err)
	}
	if string(st.RoomID) != "FFFFFF" {
		t.Errorf("碰撞重试后房间码 = %q, want %q", st.RoomID, "FFFFFF")
	}
	assertDeconfusedCode(t, string(st.RoomID))
}

// TestCreateRoomRandomCodeExhausted 验证随机码重试耗尽返回明确错误。
func TestCreateRoomRandomCodeExhausted(t *testing.T) {
	reg := &fakeLobbyRegistry{taken: map[RoomID]bool{"AAAAAA": true}}
	svc := newTestLobby(t, reg, &scriptedRNG{values: []int{
		0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0,
	}})
	svc.maxCodeTries = 2
	_, _, err := svc.CreateRoom(context.Background(), lobbyRequest())
	if !errors.Is(err, ErrCodeExhausted) {
		t.Fatalf("CreateRoom error = %v, want ErrCodeExhausted", err)
	}
}

// TestCreateRoomCustomCodeNormalizedUppercase 验证自定义码大小写混合时
// 统一规范化为大写存储与显示（docs「房间码规范」2）。
func TestCreateRoomCustomCodeNormalizedUppercase(t *testing.T) {
	var reserved []RoomID
	reg := &fakeLobbyRegistry{reserve: func(_ context.Context, code RoomID) (bool, error) {
		reserved = append(reserved, code)
		return true, nil
	}}
	svc := newTestLobby(t, reg, panicRNG{})
	req := lobbyRequest()
	req.CustomCode = "abC12"
	st, _, err := svc.CreateRoom(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateRoom error = %v, want nil", err)
	}
	if string(st.RoomID) != "ABC12" {
		t.Errorf("房间码 = %q, want 大写 %q", st.RoomID, "ABC12")
	}
	if len(reserved) != 1 || string(reserved[0]) != "ABC12" {
		t.Errorf("ReserveCode 收到的码 = %v, want [ABC12]", reserved)
	}
}

// TestCreateRoomCustomCodeTakenRejectedNoRandomFallback 验证自定义码重名时
// 明确拒绝（已被占用），绝不偷偷替换成随机码（docs「房间码规范」3）。
func TestCreateRoomCustomCodeTakenRejectedNoRandomFallback(t *testing.T) {
	reg := &fakeLobbyRegistry{taken: map[RoomID]bool{"ABC12": true}}
	svc := newTestLobby(t, reg, panicRNG{})
	req := lobbyRequest()
	req.CustomCode = "abc12"
	st, effects, err := svc.CreateRoom(context.Background(), req)
	if !errors.Is(err, ErrRoomCodeTaken) {
		t.Fatalf("CreateRoom error = %v, want ErrRoomCodeTaken", err)
	}
	if st.RoomID != "" || len(effects) != 0 {
		t.Errorf("拒绝后产生了部分新状态/Effects: state=%+v effects=%v", st, effects)
	}
}

// TestCreateRoomInvalidCustomCode 验证非法自定义码被拒绝：
// 长度必须 4～8 位且仅含字母数字。
func TestCreateRoomInvalidCustomCode(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{"过短", "ABC"},
		{"过长", "ABCDEFGHI"},
		{"非法字符", "AB-C"},
		{"内部空格", "AB 12"},
		{"纯符号", "@@@@"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestLobby(t, &fakeLobbyRegistry{}, panicRNG{})
			req := lobbyRequest()
			req.CustomCode = tc.code
			_, _, err := svc.CreateRoom(context.Background(), req)
			if !errors.Is(err, ErrInvalidRoomCode) {
				t.Fatalf("CreateRoom(%q) error = %v, want ErrInvalidRoomCode", tc.code, err)
			}
		})
	}
}

// TestCreateRoomReserveErrorPropagates 验证唯一性注册表错误向上传播。
func TestCreateRoomReserveErrorPropagates(t *testing.T) {
	reg := &fakeLobbyRegistry{reserve: func(_ context.Context, _ RoomID) (bool, error) {
		return false, errors.New("storage down")
	}}
	svc := newTestLobby(t, reg, &scriptedRNG{values: []int{0, 0, 0, 0, 0, 0}})
	_, _, err := svc.CreateRoom(context.Background(), lobbyRequest())
	if err == nil || !strings.Contains(err.Error(), "storage down") {
		t.Fatalf("CreateRoom error = %v, want 包含 storage down", err)
	}
}

// TestNormalizeRoomCode 验证自定义码规范化：去首尾空白 + 统一大写。
func TestNormalizeRoomCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abc12", "ABC12"},
		{"  AbC  ", "ABC"},
		{"a1b2c3d4", "A1B2C3D4"},
	}
	for _, tc := range cases {
		if got := NormalizeRoomCode(tc.in); got != tc.want {
			t.Errorf("NormalizeRoomCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestValidCustomRoomCode 验证自定义码合法性边界：4～8 位字母数字。
func TestValidCustomRoomCode(t *testing.T) {
	for _, code := range []string{"ABCD", "A1B2C3D4", "ABC12", "1234", "A1B2"} {
		if !ValidCustomRoomCode(code) {
			t.Errorf("ValidCustomRoomCode(%q) = false, want true", code)
		}
	}
	for _, code := range []string{"ABC", "ABCDEFGHI", "Z9", "AB-C", "", "AB 12", "ab_cd", "a1b2"} {
		if ValidCustomRoomCode(code) {
			t.Errorf("ValidCustomRoomCode(%q) = true, want false", code)
		}
	}
}

// TestGenerateRoomCodeDeconfusedAlphabet 验证随机码字符集逐字符来自
// 去混淆字母表，且字母表本身不含 0/O、1/I。
func TestGenerateRoomCodeDeconfusedAlphabet(t *testing.T) {
	if strings.ContainsAny(lobbyRoomCodeAlphabet, "01IO") {
		t.Errorf("去混淆字符集 %q 含易混淆字符 0/O/1/I", lobbyRoomCodeAlphabet)
	}
	for i := 0; i < len(lobbyRoomCodeAlphabet); i++ {
		rng := &scriptedRNG{values: []int{i, i, i, i, i, i}}
		code, err := GenerateRoomCode(rng, RandomRoomCodeLength)
		if err != nil {
			t.Fatalf("GenerateRoomCode error = %v, want nil", err)
		}
		assertDeconfusedCode(t, string(code))
	}
}
