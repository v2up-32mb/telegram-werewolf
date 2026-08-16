package app

// Task 42 场景数据层（实施计划 Task 42）：testdata/scenarios/*.yaml 的
// 加载与合法性校验。场景用「角色选择器」表达动作意图，由执行器在发牌后
// 依据真实座位解析（与 p1_e2e_test.go「以实发为准驱动场景」一致），
// 保证同一 YAML 在不同发牌排列下仍可复现。
//
// 选择器词汇（按存活玩家座位升序取第 N 个）：
//   wolf_1/wolf_2   存活狼人第 1/2 只
//   seer/witch      预言家/女巫（本局唯一）
//   villager_1/2    存活村民第 1/2 名
//   good_1..good_4  存活好人阵营（神职+村民）第 1..4 名
//   none/abstain    特殊：不使用/弃权
//
// 步骤类型：night（夜间窗口动作/超时/恶意退出）、day（白天投票/平票/
// 遗言）、settle（结算与再来一局）、abort（进程重启中止）。

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// scenario 是一个六人局端到端场景的声明式描述。
type scenario struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Seed        int64          `yaml:"seed"`
	RoomCode    string         `yaml:"room_code"`
	Config      scenarioConfig `yaml:"config"`
	Script      []scenarioStep `yaml:"script"`
}

// scenarioConfig 是场景的房间配置（MVP 边界：仅 6 人屠城）。
type scenarioConfig struct {
	Victory           string `yaml:"victory"`
	RevealRoleOnDeath bool   `yaml:"reveal_role_on_death"`
}

// scenarioStep 是场景脚本中的一步。
type scenarioStep struct {
	Step string `yaml:"step"` // night | day | settle | abort

	// night 字段
	Night         int    `yaml:"night"`
	WolfKill      string `yaml:"wolf_kill"`
	WolfTimeout   bool   `yaml:"wolf_timeout"`
	WitchSave     *bool  `yaml:"witch_save"`
	WitchPoison   string `yaml:"witch_poison"`
	WitchTimeout  bool   `yaml:"witch_timeout"`
	SeerCheck     string `yaml:"seer_check"`
	SeerExpect    string `yaml:"seer_expect"`
	SeerTimeout   bool   `yaml:"seer_timeout"`
	MaliciousExit string `yaml:"malicious_exit"`

	// day 字段
	Day         int            `yaml:"day"`
	Votes       []scenarioVote `yaml:"votes"`
	VoteTimeout bool           `yaml:"vote_timeout"`
	Tie         bool           `yaml:"tie"`
	TieRounds   []tieRound     `yaml:"tie_rounds"`
	LastWords   string         `yaml:"last_words"`

	// settle / abort 字段
	ExpectWinner         string `yaml:"expect_winner"`
	Rematch              bool   `yaml:"rematch"`
	ExpectAborted        bool   `yaml:"expect_aborted"`
	ExpectScoreUnchanged bool   `yaml:"expect_score_unchanged"`
}

// scenarioVote 是一票意图：voter/target 均为角色选择器或特殊值。
type scenarioVote struct {
	Voter  string `yaml:"voter"`
	Target string `yaml:"target"`
}

// tieRound 是平票流程的一轮：投票（首次/缩圈/最终对决）、加时发言超时、
// 无发言轮超时或最终对决超时，四者互斥选择。
type tieRound struct {
	Votes         []scenarioVote `yaml:"votes"`
	SpeechTimeout bool           `yaml:"speech_timeout"`
	RoundTimeout  bool           `yaml:"round_timeout"`
	FinalTimeout  bool           `yaml:"final_timeout"`
}

// scenarioSelectors 是选择器词汇表（校验用）。
var scenarioSelectors = map[string]bool{
	"none": true, "abstain": true,
	"wolf_1": true, "wolf_2": true,
	"seer": true, "witch": true,
	"villager_1": true, "villager_2": true,
	"good_1": true, "good_2": true, "good_3": true, "good_4": true,
}

// loadScenario 从 YAML 文件加载场景（文件不存在/解析失败返回错误）。
func loadScenario(path string) (*scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scenario: read %s: %w", path, err)
	}
	var s scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("scenario: parse %s: %w", path, err)
	}
	return &s, nil
}

// validate 校验场景合法性：步骤顺序（night 自 1 递增、day 自 1 递增、
// settle/abort 收尾）、夜间窗口互斥（动作或超时二选一）、选择器词汇、
// 平票轮互斥与结算断言取值。
func (s *scenario) validate() error {
	if s.Name == "" {
		return fmt.Errorf("scenario: name 缺失")
	}
	if s.RoomCode == "" {
		return fmt.Errorf("scenario %s: room_code 缺失", s.Name)
	}
	if s.Config.Victory != "slaughter" {
		return fmt.Errorf("scenario %s: victory %q 不支持（MVP 仅 slaughter）", s.Name, s.Config.Victory)
	}
	if len(s.Script) == 0 {
		return fmt.Errorf("scenario %s: script 为空", s.Name)
	}
	last := s.Script[len(s.Script)-1]
	if last.Step != "settle" && last.Step != "abort" {
		return fmt.Errorf("scenario %s: 最后一步必须是 settle 或 abort，实际 %q", s.Name, last.Step)
	}
	nextNight, nextDay := 1, 1
	for i, st := range s.Script {
		switch st.Step {
		case "night":
			if st.Night != nextNight {
				return fmt.Errorf("scenario %s: 第 %d 步 night 序号 %d，期望 %d", s.Name, i+1, st.Night, nextNight)
			}
			nextNight++
			if err := validateNightStep(s, st); err != nil {
				return fmt.Errorf("scenario %s 第 %d 步: %w", s.Name, i+1, err)
			}
		case "day":
			if st.Day != nextDay {
				return fmt.Errorf("scenario %s: 第 %d 步 day 序号 %d，期望 %d", s.Name, i+1, st.Day, nextDay)
			}
			nextDay++
			if err := validateDayStep(s, st); err != nil {
				return fmt.Errorf("scenario %s 第 %d 步: %w", s.Name, i+1, err)
			}
		case "settle":
			if st.ExpectWinner != "good" && st.ExpectWinner != "wolf" && st.ExpectWinner != "any" {
				return fmt.Errorf("scenario %s: expect_winner %q 非法（good/wolf/any）", s.Name, st.ExpectWinner)
			}
		case "abort":
			if !st.ExpectAborted {
				return fmt.Errorf("scenario %s: abort 步必须 expect_aborted=true", s.Name)
			}
		default:
			return fmt.Errorf("scenario %s: 未知步骤类型 %q", s.Name, st.Step)
		}
	}
	return nil
}

// validateNightStep 校验夜间步骤：狼/巫/预三窗口必须且只能选择
// 「显式动作」或「超时」之一；恶意退出目标必须是合法选择器。
func validateNightStep(s *scenario, st scenarioStep) error {
	wolfAct := st.WolfKill != "" && st.WolfKill != "none"
	if wolfAct == st.WolfTimeout {
		return fmt.Errorf("night %d: wolf_kill 与 wolf_timeout 必须二选一", st.Night)
	}
	if wolfAct && !scenarioSelectors[st.WolfKill] {
		return fmt.Errorf("night %d: 非法 wolf_kill 选择器 %q", st.Night, st.WolfKill)
	}
	witchAct := st.WitchSave != nil || st.WitchPoison != ""
	if witchAct == st.WitchTimeout {
		return fmt.Errorf("night %d: witch 动作与 witch_timeout 必须二选一", st.Night)
	}
	if st.WitchSave != nil && st.WitchPoison == "" {
		return fmt.Errorf("night %d: witch 动作必须同时给出 witch_save 与 witch_poison", st.Night)
	}
	if st.WitchPoison != "" && !scenarioSelectors[st.WitchPoison] {
		return fmt.Errorf("night %d: 非法 witch_poison 选择器 %q", st.Night, st.WitchPoison)
	}
	seerAct := st.SeerCheck != ""
	if seerAct == st.SeerTimeout {
		return fmt.Errorf("night %d: seer_check 与 seer_timeout 必须二选一", st.Night)
	}
	if seerAct && !scenarioSelectors[st.SeerCheck] {
		return fmt.Errorf("night %d: 非法 seer_check 选择器 %q", st.Night, st.SeerCheck)
	}
	if st.SeerExpect != "" && st.SeerExpect != "wolf" && st.SeerExpect != "good" {
		return fmt.Errorf("night %d: seer_expect %q 非法（wolf/good）", st.Night, st.SeerExpect)
	}
	if st.MaliciousExit != "" && !scenarioSelectors[st.MaliciousExit] {
		return fmt.Errorf("night %d: 非法 malicious_exit 选择器 %q", st.Night, st.MaliciousExit)
	}
	return nil
}

// validateDayStep 校验白天步骤：投票与超时二选一；平票轮互斥；
// 投票目标允许 abstain（最终对决禁止弃权由 game 层强制）；遗言为文本或 none。
func validateDayStep(s *scenario, st scenarioStep) error {
	if st.Tie {
		if len(st.TieRounds) == 0 {
			return fmt.Errorf("day %d: tie 步骤必须提供 tie_rounds", st.Day)
		}
		for i, r := range st.TieRounds {
			n := countSet(r.Votes != nil, r.SpeechTimeout, r.RoundTimeout, r.FinalTimeout)
			if n != 1 {
				return fmt.Errorf("day %d tie_rounds[%d]: 投票/加时超时/无发言超时/最终超时必须恰选其一", st.Day, i)
			}
			for _, v := range r.Votes {
				if !scenarioSelectors[v.Voter] {
					return fmt.Errorf("day %d tie_rounds[%d]: 非法 voter %q", st.Day, i, v.Voter)
				}
				if !scenarioSelectors[v.Target] {
					return fmt.Errorf("day %d tie_rounds[%d]: 非法 target %q", st.Day, i, v.Target)
				}
			}
		}
		return nil
	}
	if (len(st.Votes) == 0) != st.VoteTimeout {
		return fmt.Errorf("day %d: votes 与 vote_timeout 必须二选一", st.Day)
	}
	for _, v := range st.Votes {
		if !scenarioSelectors[v.Voter] {
			return fmt.Errorf("day %d: 非法 voter %q", st.Day, v.Voter)
		}
		if !scenarioSelectors[v.Target] {
			return fmt.Errorf("day %d: 非法 target %q", st.Day, v.Target)
		}
	}
	if st.LastWords == "" {
		return fmt.Errorf("day %d: 缺少 last_words（文本或 none）", st.Day)
	}
	return nil
}

// countSet 统计布尔条件中为真的个数。
func countSet(vals ...bool) int {
	n := 0
	for _, v := range vals {
		if v {
			n++
		}
	}
	return n
}

// TestScenarioDataValid 校验四个内置场景文件均可加载且通过合法性校验。
func TestScenarioDataValid(t *testing.T) {
	for _, path := range []string{
		"../../testdata/scenarios/good_win.yaml",
		"../../testdata/scenarios/wolf_win.yaml",
		"../../testdata/scenarios/tie_vote.yaml",
		"../../testdata/scenarios/restart_abort.yaml",
	} {
		s, err := loadScenario(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		if err := s.validate(); err != nil {
			t.Fatalf("validate %s: %v", path, err)
		}
		if strings.TrimSpace(s.Description) == "" {
			t.Fatalf("scenario %s: description 缺失", s.Name)
		}
	}
}
