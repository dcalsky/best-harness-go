package main

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dcalsky/best-harness-go"
)

func newDemoProvider() harness.Provider {
	return &harness.Faux{StreamFunc: func(_ context.Context, request harness.Request) (harness.Stream, error) {
		if len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == harness.RoleTool {
			return demoTextStream("图表已经生成，你可以先预览，满意后点击按钮添加到仪表盘。"), nil
		}
		text := ""
		for i := len(request.Messages) - 1; i >= 0; i-- {
			if request.Messages[i].Role == harness.RoleUser {
				text = request.Messages[i].Text()
				break
			}
		}
		params := demoChart(strings.ToLower(text))
		arguments, _ := json.Marshal(params)
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventStart},
			{Type: harness.EventThinkingDelta, Text: "正在选择合适的图表类型和示例数据。"},
			{Type: harness.EventToolCallStart, Index: 0, ToolCallID: string(harness.NewID()), ToolName: "render_chart"},
			{Type: harness.EventToolCallDelta, Index: 0, ArgumentsDelta: string(arguments)},
			{Type: harness.EventToolCallEnd, Index: 0},
			{Type: harness.EventDone, StopReason: harness.StopToolUse},
		}}, nil
	}}
}

func demoTextStream(text string) harness.Stream {
	runes := []rune(text)
	middle := len(runes) / 2
	return &harness.SliceStream{Events: []harness.StreamEvent{
		{Type: harness.EventStart},
		{Type: harness.EventTextDelta, Text: string(runes[:middle])},
		{Type: harness.EventTextDelta, Text: string(runes[middle:])},
		{Type: harness.EventDone, StopReason: harness.StopStop},
	}}
}

func demoChart(text string) renderChartParams {
	switch {
	case strings.Contains(text, "pie") || strings.Contains(text, "饼") || strings.Contains(text, "占比"):
		return renderChartParams{Title: "渠道销售占比", ChartType: "pie", Description: "各渠道对总销售额的贡献", Option: map[string]any{
			"tooltip": map[string]any{"trigger": "item"}, "legend": map[string]any{"bottom": 0},
			"series": []any{map[string]any{"type": "pie", "radius": []any{"42%", "70%"}, "data": []any{
				map[string]any{"name": "线上商城", "value": 42}, map[string]any{"name": "线下门店", "value": 28},
				map[string]any{"name": "合作伙伴", "value": 18}, map[string]any{"name": "企业客户", "value": 12},
			}}},
		}}
	case strings.Contains(text, "scatter") || strings.Contains(text, "散点") || strings.Contains(text, "相关"):
		return renderChartParams{Title: "投入与转化相关性", ChartType: "scatter", Description: "观察营销投入和订单转化之间的关系", Option: map[string]any{
			"tooltip": map[string]any{"trigger": "item"}, "xAxis": map[string]any{"name": "投入（千元）"}, "yAxis": map[string]any{"name": "转化率（%）"},
			"series": []any{map[string]any{"type": "scatter", "symbolSize": 14, "data": []any{[]any{12, 2.4}, []any{18, 3.1}, []any{23, 3.8}, []any{31, 5.2}, []any{38, 5.7}, []any{46, 7.1}, []any{55, 7.8}}}},
		}}
	case strings.Contains(text, "radar") || strings.Contains(text, "雷达") || strings.Contains(text, "能力"):
		return renderChartParams{Title: "团队能力雷达", ChartType: "radar", Description: "核心能力维度对比", Option: map[string]any{
			"legend": map[string]any{"bottom": 0, "data": []any{"本季度", "上季度"}},
			"radar":  map[string]any{"indicator": []any{map[string]any{"name": "交付", "max": 100}, map[string]any{"name": "创新", "max": 100}, map[string]any{"name": "质量", "max": 100}, map[string]any{"name": "协作", "max": 100}, map[string]any{"name": "增长", "max": 100}}},
			"series": []any{map[string]any{"type": "radar", "data": []any{map[string]any{"name": "本季度", "value": []any{88, 76, 91, 84, 79}}, map[string]any{"name": "上季度", "value": []any{76, 70, 86, 78, 68}}}}},
		}}
	case strings.Contains(text, "gauge") || strings.Contains(text, "仪表") || strings.Contains(text, "完成率"):
		return renderChartParams{Title: "年度目标完成率", ChartType: "gauge", Description: "当前目标达成进度", Option: map[string]any{
			"series": []any{map[string]any{"type": "gauge", "progress": map[string]any{"show": true, "width": 14}, "axisLine": map[string]any{"lineStyle": map[string]any{"width": 14}}, "detail": map[string]any{"valueAnimation": true, "formatter": "{value}%"}, "data": []any{map[string]any{"value": 78, "name": "目标完成"}}}},
		}}
	case strings.Contains(text, "bar") || strings.Contains(text, "柱") || strings.Contains(text, "排名"):
		return renderChartParams{Title: "区域销售排名", ChartType: "bar", Description: "本季度各区域销售额对比", Option: map[string]any{
			"tooltip": map[string]any{"trigger": "axis"}, "grid": map[string]any{"left": 24, "right": 20, "top": 20, "bottom": 20, "containLabel": true},
			"xAxis": map[string]any{"type": "value"}, "yAxis": map[string]any{"type": "category", "data": []any{"华南", "华北", "华东", "西南", "西北"}},
			"series": []any{map[string]any{"type": "bar", "data": []any{86, 94, 128, 72, 58}, "barWidth": 18}},
		}}
	default:
		return renderChartParams{Title: "月度活跃用户趋势", ChartType: "line", Description: "近半年活跃用户数及增长走势", Option: map[string]any{
			"tooltip": map[string]any{"trigger": "axis"}, "grid": map[string]any{"left": 24, "right": 20, "top": 20, "bottom": 20, "containLabel": true},
			"xAxis": map[string]any{"type": "category", "boundaryGap": false, "data": []any{"3月", "4月", "5月", "6月", "7月", "8月"}}, "yAxis": map[string]any{"type": "value"},
			"series": []any{map[string]any{"name": "活跃用户", "type": "line", "smooth": true, "areaStyle": map[string]any{"opacity": 0.12}, "data": []any{32, 41, 45, 58, 67, 82}}},
		}}
	}
}
