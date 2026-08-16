package game

// Report 是结算战报（docs 游戏流程设计.md §结算 7、§记录 243、阶段消息
// 设计.md §15）：胜方、参与人（全员身份翻牌与结果/积分变化）与关键事件。
// 战报不伪装完整回放（关键事件只含最终状态可推导条目）；不评选最佳玩家
// （docs §结算 8）。
type Report struct {
	Winner    Camp
	Players   []PlayerResult
	KeyEvents []KeyEvent
}

// BuildReport 由已结算状态构建战报；未结算返回 ErrNotSettled。
func BuildReport(st State) (Report, error) {
	if len(st.Settled.Revealed) == 0 ||
		(st.Settled.Winner != CampWolf && st.Settled.Winner != CampGood) {
		return Report{}, ErrNotSettled
	}
	return Report{
		Winner:    st.Settled.Winner,
		Players:   st.Settled.Revealed,
		KeyEvents: st.Settled.KeyEvents,
	}, nil
}
