package model

import (
	"testing"
	"time"
)

func TestProOAICustomMarkersAndReviewDue(t *testing.T) {
	rule := ProOAIInquiry{Prompt: "{{email}} {{success_marker}} {{failure_marker}} {{review_marker}}", SuccessMarker: "确认完成!", FailureMarker: "未处理", ReviewMarker: "未知"}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := rule.Question("child@example.com"); got != "child@example.com 确认完成! 未处理 未知" {
		t.Fatal(got)
	}
	for _, tc := range []struct{ answer, want string }{{"退款已处理。\n确认完成!", "confirmed"}, {"未找到退款记录。\n未处理", "unconfirmed"}, {"无法查询。\n未知", "review"}, {"退款已处理。\n已成功", "review"}, {"无法确认退款已处理。\n确认完成!", "review"}} {
		if got := ProOAIAnswerStatus("child@example.com", tc.answer, rule); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	rule.FailureMarker = rule.SuccessMarker
	if rule.Validate() == nil {
		t.Fatal("duplicate markers accepted")
	}
	past := time.Now().Add(-time.Hour)
	v := &ProOAIConfirmation{Status: "review", CompletedAt: past, CheckedAt: &past, Attempts: 1, MaxAttempts: 3, RetryIntervalSeconds: 300}
	if !v.Due(time.Now()) {
		t.Fatal("legacy review never retries")
	}
	v.Attempts = 3
	if v.Due(time.Now()) {
		t.Fatal("review ignores attempt cap")
	}
}

func TestProOAIAnswerStatus(t *testing.T) {
	for _, tc := range []struct{ name, answer, want string }{
		{"recorded answer", "已确认该订阅（child@example.com）对应的退款已发起/处理；预计 5–10 个工作日；发票号 TEST-0001。\n已成功", "confirmed"},
		{"formatting", "退款已处理。\n**已成功**", "confirmed"},
		{"not bank arrival", "已发起退款，银行尚未到账。如果超期请联系银行。\n已成功", "confirmed"},
		{"negative", "未找到退款记录。\n已失败", "unconfirmed"},
		{"marker only", "已成功", "review"},
		{"prompt echoed", "如果是退款已发起/处理请在最后一行输入:已成功\n如果是退款未发起或没有请在最后一行输入:已失败", "review"},
		{"contradiction", "退款已处理，但无法确认退款记录。\n已成功", "review"},
		{"not submitted", "退款尚未发起，此前退款已完成。\n已成功", "review"},
		{"conditional", "如果退款已处理，通常需要五天。\n已成功", "review"},
		{"other account", "other@example.com 退款已完成。\n已成功", "review"},
		{"partial", "退款已完成。", "review"},
		{"unknown", "当前无法查询。\n待核实", "review"},
		{"suffix", "退款已处理。\n已成功但未查询", "review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProOAIAnswerStatus("child@example.com", tc.answer); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestProOAIProducedRefundAnswer(t *testing.T) {
	for _, tc := range []struct{ name, answer, want string }{
		{"recorded wording", "我是 OpenAI 的 AI 辅助支持。\n\n我这边看到你的账户（child@example.com）有一笔付款 5794.64 PHP，对应发票号 **TEST-0001**，并且系统记录显示该笔款项**已产生退款**。退款入账时间通常为 **5–10 个工作日**（具体以发卡行/银行处理速度为准）。\n\n已成功", "confirmed"},
		{"one refund", "系统记录显示已产生了一笔退款，银行尚未到账。\n已成功", "confirmed"},
		{"refund subject", "这笔退款已经产生，预计五个工作日入账。\n已成功", "confirmed"},
		{"fee refund", "系统记录显示已产生退费。\n**已成功**", "confirmed"},
		{"no marker", "系统记录显示已产生退款。", "review"},
		{"wrong account", "other@example.com 已产生退款。\n已成功", "review"},
		{"cannot verify", "无法确认是否已产生退款。\n已成功", "review"},
		{"conditional", "如果已产生退款，通常五天到账。\n已成功", "review"},
		{"conditional subject", "假如退款已经产生，通常五天到账。\n已成功", "review"},
		{"negated", "这不代表已产生退款。\n已成功", "review"},
		{"explicit denial", "并非已产生退款。\n已成功", "review"},
		{"not yet", "尚未产生退款。\n已成功", "review"},
		{"conflicting prior refund", "之前已产生退款，但当前这笔尚未产生退款。\n已成功", "review"},
		{"conflicting subject", "之前退款已完成，但当前退款还未产生。\n已成功", "review"},
		{"failure marker", "尚未产生退款。\n已失败", "unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProOAIAnswerStatus("child@example.com", tc.answer); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	rule := ProOAIInquiry{SuccessMarker: "确认完成!", FailureMarker: "未处理", ReviewMarker: "未知"}
	if got := ProOAIAnswerStatus("child@example.com", "系统记录显示已产生退款。\n确认完成!", rule); got != "confirmed" {
		t.Fatalf("custom marker: got %s", got)
	}
}
