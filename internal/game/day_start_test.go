package game

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

// dayTestState 构造 PhaseDaySpeech 的 6 人状态：座位 3/5 已死亡。
// 牌组与死者角色由调用方指定，保证断言确定。
func dayTestState(roles map[Seat]Role) State {
	st := State{
		RoomID:       RoomID("DAY34"),
		Phase:        PhaseDaySpeech,
		PhaseVersion: 4,
		Settings:     DefaultRoomSettings(),
	}
	seats := []Seat{1, 2, 3, 4, 5, 6}
	for i, s := range seats {
		st.Players = append(st.Players, Player{
			UserID: UserID(900 + i),
			Seat:   s,
			Role:   roles[s],
			Dead:   s == 3 || s == 5,
		})
	}
	return st
}

func dayRoles() map[Seat]Role {
	return map[Seat]Role{
		1: RoleVillager, 2: RoleVillager, 3: RoleWolf, 4: RoleSeer, 5: RoleWitch, 6: RoleWolf,
	}
}

// TestDayStartDefaultNoIdentityNoCause 验证默认配置（RevealRoleOnDeath=false）：
// 公共死讯只含 victims，不含身份、不含死因；每名死者有本人私聊说明
// （真实身份 + 真实死因），且死因绝不进入公共 params。
func TestDayStartDefaultNoIdentityNoCause(t *testing.T) {
	st := dayTestState(dayRoles())
	out := DayOutcome{
		Victims: []Seat{3, 5},
		Cause:   map[Seat]DeathCause{3: CauseWolfKill, 5: CauseWitchPoison},
	}
	next, effects, err := DayStart(st, out)
	if err != nil {
		t.Fatalf("DayStart error = %v", err)
	}
	if !reflect.DeepEqual(next, st) {
		t.Errorf("DayStart 不得改动状态: next != st")
	}
	var public, priv3, priv5 *MessageEffect
	for _, e := range effects {
		me, ok := e.(MessageEffect)
		if !ok {
			t.Errorf("非消息效果出现: %#v", e)
			continue
		}
		switch {
		case me.Key == DayDeathMessageKey:
			if me.Audience != AudiencePublic {
				t.Errorf("day.death 受众 = %v, want public", me.Audience)
			}
			public = &me
		case me.Key == DayDeathPrivateMessageKey:
			if me.Audience != AudienceActor {
				t.Errorf("day.death_private 受众 = %v, want actor", me.Audience)
			}
			switch me.Params["seat"] {
			case Seat(3):
				priv3 = &me
			case Seat(5):
				priv5 = &me
			}
		default:
			t.Errorf("未知消息 key %q", me.Key)
		}
	}
	if public == nil {
		t.Fatal("缺少公共 day.death")
	}
	victims, ok := public.Params["victims"].([]Seat)
	if !ok || !reflect.DeepEqual(victims, []Seat{3, 5}) {
		t.Fatalf("day.death victims = %v, want [3 5]", public.Params["victims"])
	}
	if _, has := public.Params["roles"]; has {
		t.Errorf("默认配置下公共死讯不得含 roles")
	}
	if _, has := public.Params["cause"]; has {
		t.Errorf("公共死讯不得含 cause")
	}
	if priv3 == nil || priv5 == nil {
		t.Fatalf("死者私聊缺失: priv3=%v priv5=%v", priv3 != nil, priv5 != nil)
	}
	if priv3.Params["role"] != RoleWolf || priv3.Params["cause"] != CauseWolfKill {
		t.Errorf("3 号私聊 = role=%v cause=%v, want 狼人/wolf_kill", priv3.Params["role"], priv3.Params["cause"])
	}
	if priv5.Params["role"] != RoleWitch || priv5.Params["cause"] != CauseWitchPoison {
		t.Errorf("5 号私聊 = role=%v cause=%v, want 女巫/witch_poison", priv5.Params["role"], priv5.Params["cause"])
	}
}

// TestDayStartRevealRoleOnDeath 验证房主开启报身份后公共死讯附带死者身份，
// 但依然不含死因。
func TestDayStartRevealRoleOnDeath(t *testing.T) {
	st := dayTestState(dayRoles())
	st.Settings.RevealRoleOnDeath = true
	out := DayOutcome{Victims: []Seat{3, 5}, Cause: map[Seat]DeathCause{3: CauseWolfKill}}
	_, effects, err := DayStart(st, out)
	if err != nil {
		t.Fatalf("DayStart error = %v", err)
	}
	var public *MessageEffect
	for _, e := range effects {
		if me, ok := e.(MessageEffect); ok && me.Key == DayDeathMessageKey {
			public = &me
		}
	}
	if public == nil {
		t.Fatal("缺少公共 day.death")
	}
	roles, ok := public.Params["roles"].(map[Seat]Role)
	if !ok {
		t.Fatalf("报身份配置下 day.death 应含 roles，got %T", public.Params["roles"])
	}
	if roles[3] != RoleWolf || roles[5] != RoleWitch {
		t.Errorf("roles = %v, want 3=wolf 5=witch", roles)
	}
	if _, has := public.Params["cause"]; has {
		t.Errorf("公共死讯不得含 cause")
	}
}

// TestDayStartPeaceNight 验证平安夜只产生公共 day.peace。
func TestDayStartPeaceNight(t *testing.T) {
	st := dayTestState(dayRoles())
	_, effects, err := DayStart(st, DayOutcome{})
	if err != nil {
		t.Fatalf("DayStart error = %v", err)
	}
	if len(effects) != 1 {
		t.Fatalf("平安夜效果数 = %d, want 1", len(effects))
	}
	me, ok := effects[0].(MessageEffect)
	if !ok || me.Key != DayPeaceMessageKey || me.Audience != AudiencePublic {
		t.Fatalf("平安夜效果 = %#v, want public day.peace", effects[0])
	}
}

// TestDayStartWrongPhase 验证非 PhaseDaySpeech 时返回明确错误且不改状态。
func TestDayStartWrongPhase(t *testing.T) {
	st := dayTestState(dayRoles())
	st.Phase = PhaseNight
	_, _, err := DayStart(st, DayOutcome{Victims: []Seat{3}})
	if !errors.Is(err, ErrDayNotInDayPhase) {
		t.Fatalf("err = %v, want ErrDayNotInDayPhase", err)
	}
}

// TestDayStartPrivateOnlyForVictims 验证私聊说明只发给死者本人（按 seat 推导），
// 存活玩家不产生任何 day.death_private。
func TestDayStartPrivateOnlyForVictims(t *testing.T) {
	st := dayTestState(dayRoles())
	_, effects, err := DayStart(st, DayOutcome{Victims: []Seat{5}, Cause: map[Seat]DeathCause{5: CauseWitchPoison}})
	if err != nil {
		t.Fatalf("DayStart error = %v", err)
	}
	priv := 0
	for _, e := range effects {
		if me, ok := e.(MessageEffect); ok && me.Key == DayDeathPrivateMessageKey {
			priv++
			if me.Params["seat"] != Seat(5) {
				t.Errorf("私聊 seat = %v, want 5", me.Params["seat"])
			}
		}
	}
	if priv != 1 {
		t.Fatalf("day.death_private 数量 = %d, want 1", priv)
	}
}

// TestDayStartVictimsSortedAscending 验证受害者名单升序（渲染稳定）。
func TestDayStartVictimsSortedAscending(t *testing.T) {
	st := dayTestState(dayRoles())
	out := DayOutcome{Victims: []Seat{5, 3, 4}, Cause: map[Seat]DeathCause{3: CauseWolfKill, 4: CauseWitchPoison, 5: CauseWolfKill}}
	_, effects, err := DayStart(st, out)
	if err != nil {
		t.Fatalf("DayStart error = %v", err)
	}
	var victims []Seat
	for _, e := range effects {
		if me, ok := e.(MessageEffect); ok && me.Key == DayDeathMessageKey {
			victims, _ = me.Params["victims"].([]Seat)
		}
	}
	want := []Seat{3, 4, 5}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(victims, want) {
		t.Fatalf("victims = %v, want 升序 %v", victims, want)
	}
}
