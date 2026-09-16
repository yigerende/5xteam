package store

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestMailGPTInfoPersistenceAndMerge(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	email := "info@example.com"
	creds := model.MailAccountCredentials{Email: email, AccessToken: "old-at", RefreshToken: "keep-rt", GptPassword: "keep-password", TotpSecret: "keep-totp"}
	profile, err := st.SaveMailAccount(model.MailAccountProfile{Email: email, Group: "free"}, creds)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1700000000, 0).UTC()
	result := model.MailGPTInfoResult{
		Check: model.GPTInfoCheck{Status: "success", CheckedAt: time.Now().UTC(), PlanSource: "entitlement", Plan: model.GPTInfoRequestStatus{OK: true, HTTPStatus: 200}, Me: model.GPTInfoRequestStatus{OK: true, HTTPStatus: 200}},
		Plan:  model.AccountPlanCheckResult{OK: true, CurrentPlanType: "pro", SubscriptionPlan: "chatgptproplan"}, CreatedAtOpenAI: &created,
	}
	got, err := st.SaveMailGPTInfo(email, profile.ID, "old-at", result)
	if err != nil || got.CurrentPlanType != "pro" || got.CreatedAtOpenAI == nil || !got.CreatedAt.Equal(profile.CreatedAt) {
		t.Fatalf("save=%+v %v", got, err)
	}
	failed := model.MailGPTInfoResult{Check: model.GPTInfoCheck{Status: "failed", CheckedAt: time.Now().UTC(), PlanSource: "jwt", Plan: model.GPTInfoRequestStatus{HTTPStatus: 401, Error: "invalid AT"}, Me: model.GPTInfoRequestStatus{HTTPStatus: 403}}, Plan: model.AccountPlanCheckResult{CurrentPlanType: "free"}}
	got, err = st.SaveMailGPTInfo(email, profile.ID, "old-at", failed)
	if err != nil || got.CurrentPlanType != "pro" || !got.CreatedAtOpenAI.Equal(created) || got.GPTInfoCheck.PlanSource == "jwt" {
		t.Fatalf("failed request cleared data: %+v %v", got, err)
	}
	if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email, Label: "reimported"}, creds); err != nil {
		t.Fatal(err)
	}
	if err = st.SaveMailAccountOAuth(email, "new-at", "new-rt"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveMailGPTInfo(email, profile.ID, "old-at", result); err == nil {
		t.Fatal("accepted stale AT response")
	}
	st.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, saved, err := st.MailAccountCredential(email)
	if err != nil || got.CurrentPlanType != "pro" || got.CreatedAtOpenAI == nil || !got.CreatedAtOpenAI.Equal(created) || saved.AccessToken != "new-at" || saved.RefreshToken != "new-rt" || saved.GptPassword != "keep-password" || saved.TotpSecret != "keep-totp" {
		t.Fatalf("persistence/credentials lost: %+v %v", got, err)
	}
	if got.ChatGPTStatus == "dead" {
		t.Fatal("401/403 marked dead")
	}
	if err = st.DeleteMailAccount(email); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveMailGPTInfo(email, profile.ID, "new-at", result); err == nil {
		t.Fatal("deleted account recreated")
	}
	recreated, err := st.SaveMailAccount(model.MailAccountProfile{Email: email}, creds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveMailGPTInfo(email, profile.ID, "old-at", result); err == nil || recreated.ID == profile.ID {
		t.Fatal("old result applied to recreated account")
	}
}
func TestMailGPTInfoPartialAndJWTFallback(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveMailAccount(model.MailAccountProfile{Email: "fallback@example.com"}, model.MailAccountCredentials{AccessToken: "at"})
	if err != nil {
		t.Fatal(err)
	}
	r := model.MailGPTInfoResult{Check: model.GPTInfoCheck{Status: "failed", PlanSource: "jwt", CheckedAt: time.Now()}, Plan: model.AccountPlanCheckResult{CurrentPlanType: "free"}}
	p, err = st.SaveMailGPTInfo(p.Email, p.ID, "at", r)
	if err != nil || p.CurrentPlanType != "free" || p.GPTInfoCheck.Status != "failed" {
		t.Fatal(p, err)
	}
	created := time.Unix(1600000000, 0).UTC()
	r.CreatedAtOpenAI = &created
	r.Check.Status = "partial"
	r.Check.Me.OK = true
	p, err = st.SaveMailGPTInfo(p.Email, p.ID, "at", r)
	if err != nil || p.CreatedAtOpenAI == nil || p.CurrentPlanType != "free" {
		t.Fatal(p, err)
	}
	r.Check.PlanSource = "me"
	r.Check.Plan.HTTPStatus = 403
	r.Plan = model.AccountPlanCheckResult{OK: true, CurrentPlanType: "plus", HTTPStatus: 200}
	p, err = st.SaveMailGPTInfo(p.Email, p.ID, "at", r)
	if err != nil || p.PlanCheckHTTPStatus != 200 || p.CurrentPlanType != "plus" {
		t.Fatal(p, err)
	}
}
