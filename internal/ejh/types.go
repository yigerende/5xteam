package ejh

import (
	"chatgpt-space-merge/internal/cardplatform"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Settings struct {
	Enabled                   bool       `json:"enabled"`
	URL                       string     `json:"url"`
	AppKey                    string     `json:"app_key"`
	SecretPresent             bool       `json:"secret_present"`
	SigningKeyPresent         bool       `json:"signing_key_present"`
	SigningPublicKey          string     `json:"signing_public_key,omitempty"`
	SigningPrivateKey         string     `json:"-"`
	DefaultMaxAccounts        int        `json:"default_max_accounts"`
	SingleActiveAccount       bool       `json:"single_active_account"`
	SyncEnabled               bool       `json:"sync_enabled"`
	SyncIntervalSeconds       int        `json:"sync_interval_seconds"`
	AutoCancelEnabled         bool       `json:"auto_cancel_enabled"`
	AutoCancelIntervalSeconds int        `json:"auto_cancel_interval_seconds"`
	PageRefreshSeconds        int        `json:"page_refresh_seconds"`
	AlertHours                int        `json:"alert_hours"`
	ReversalFeePercent        float64    `json:"reversal_fee_percent"`
	Templates                 []Template `json:"templates"`
}
type Template struct {
	Platform  string `json:"platform"`
	PlanCode  string `json:"plan_code,omitempty"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ValidDays int    `json:"valid_days"`
	OpenInput
}
type OpenInput struct {
	CardType     string `json:"cardType"`
	CardAmount   string `json:"cardAmount"`
	CardCurrency string `json:"cardCurrency"`
	BillCurrency string `json:"billCurrency"`
	UseTime      int    `json:"useTime"`
	CompanyRate  string `json:"companyRate,omitempty"`
	AuthCount    *int   `json:"authCount,omitempty"`
	ActiveDate   string `json:"activeDate"`
	Note         string `json:"extInfoyx,omitempty"`
	MCC          string `json:"supportedMccGroup"`
	Title        string `json:"OrderTitle,omitempty"`
	Guest        string `json:"OrderGuestName,omitempty"`
	Start        string `json:"OrderActiveDate,omitempty"`
	End          string `json:"OrderExpiryDate,omitempty"`
	Amount       string `json:"amount,omitempty"`
}
type Card struct {
	OrderNo          string `json:"orderNo"`
	Number           string `json:"cardNo"`
	Validity         string `json:"validityDate"`
	CVV              string `json:"cvvPassword"`
	CardType         string `json:"cardType"`
	UseTime          int    `json:"useTime"`
	CardAmount       string `json:"cardAmount"`
	CardCurrency     string `json:"cardCurrency"`
	BillAmount       string `json:"billAmount"`
	BillCurrency     string `json:"billCurrency"`
	ActualAmount     string `json:"actualConvertedAmount"`
	ActualBillAmount string `json:"actualBillAmount"`
	Fee              string `json:"cardServiceFee"`
	ForexFee         string `json:"currencyForexFee"`
	Rate             string `json:"bankRate"`
	StartTime        string `json:"startTime"`
	EndTime          string `json:"endTime"`
	Note             string `json:"extInfoyx"`
	Status           string `json:"status"`
	Locked           string `json:"isLocked"`
}
type Trade struct {
	OrderNo       string `json:"orderNo"`
	Number        string `json:"cardNo,omitempty"`
	TradeNo       string `json:"tradeNo"`
	Note          string `json:"extInfoyx"`
	CardType      string `json:"cardType"`
	CardCurrency  string `json:"cardCurrency"`
	CardAmount    string `json:"cardAmount"`
	TradeCurrency string `json:"tradeCurrency"`
	TradeAmount   string `json:"tradeAmount"`
	BillCurrency  string `json:"billCurrency"`
	BillAmount    string `json:"billAmount"`
	RefundAmount  string `json:"refundBuyPrice"`
	Type          string `json:"tradeType"`
	Status        string `json:"tradeStatus"`
	AuthCode      string `json:"authCode"`
	RefundTag     string `json:"refundTag"`
	TradeTime     string `json:"tradeTime"`
	CreatedAt     string `json:"createdAt"`
	Merchant      string `json:"collectionName"`
	Address       string `json:"collectionAddress"`
	Rate          string `json:"buyTradeBankRate"`
}

func (t Trade) IsReturn() bool { return t.Type == "2" || t.Type == "5" || t.Type == "16" }
func (t Trade) Kind() string {
	if t.Type == "2" {
		return "reversal"
	}
	if t.Type == "17" {
		return "card_close"
	}
	if t.Type == "5" || t.Type == "16" {
		return "refund"
	}
	return "other"
}
func DefaultSettings() Settings {
	return Settings{URL: "https://www.ejhcard.com", DefaultMaxAccounts: 3, SingleActiveAccount: true, SyncIntervalSeconds: 300, AutoCancelIntervalSeconds: 300, PageRefreshSeconds: 30, AlertHours: 24, ReversalFeePercent: 6, Templates: []Template{}}
}

var decimal = regexp.MustCompile(`^\d{1,12}(\.\d{1,6})?$`)
var currency = regexp.MustCompile(`^[A-Z]{3}$`)

func ValidCurrency(v string) bool { return currency.MatchString(v) }

func Money(v string) (*big.Rat, error) {
	if !decimal.MatchString(v) {
		return nil, fmt.Errorf("金额格式无效（最多 6 位小数）")
	}
	n, ok := new(big.Rat).SetString(v)
	if !ok {
		return nil, fmt.Errorf("金额无效")
	}
	return n, nil
}
func Positive(v string) error {
	n, e := Money(v)
	if e != nil {
		return e
	}
	if n.Sign() <= 0 {
		return fmt.Errorf("金额必须大于 0")
	}
	return nil
}
func Normalize(v Settings) (Settings, error) {
	if v.AutoCancelIntervalSeconds == 0 {
		v.AutoCancelIntervalSeconds = 300
	}
	if v.AutoCancelIntervalSeconds < 60 || v.AutoCancelIntervalSeconds > 86400 {
		return v, fmt.Errorf("自动销卡检查间隔须为 60～86400 秒")
	}
	if v.PageRefreshSeconds == 0 {
		v.PageRefreshSeconds = 30
	}
	if v.PageRefreshSeconds < 5 || v.PageRefreshSeconds > 86400 {
		return v, fmt.Errorf("页面刷新间隔须为 5～86400 秒")
	}
	v.URL = strings.TrimRight(strings.TrimSpace(v.URL), "/")
	if v.URL == "" {
		v.URL = DefaultSettings().URL
	}
	u, e := url.Parse(v.URL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" {
		return v, fmt.Errorf("EJH 地址须为 HTTPS，不能包含密钥、查询参数或片段")
	}
	if v.DefaultMaxAccounts < 1 || v.DefaultMaxAccounts > 10000 {
		return v, fmt.Errorf("EJH 每卡上限须为 1～10000")
	}
	if v.SyncIntervalSeconds < 60 || v.SyncIntervalSeconds > 86400 {
		return v, fmt.Errorf("同步间隔须为 60～86400 秒")
	}
	if v.AlertHours < 1 || v.AlertHours > 8760 {
		return v, fmt.Errorf("提醒时长须为 1～8760 小时")
	}
	if math.IsNaN(v.ReversalFeePercent) || math.IsInf(v.ReversalFeePercent, 0) || v.ReversalFeePercent < 0 || v.ReversalFeePercent > 100 || math.Round(v.ReversalFeePercent*100)/100 != v.ReversalFeePercent {
		return v, fmt.Errorf("授权撤销费比例须为 0～100，最多两位小数")
	}
	v.AppKey = strings.TrimSpace(v.AppKey)
	if len(v.AppKey) > 200 {
		return v, fmt.Errorf("AppKey 过长")
	}
	if len(v.Templates) > 100 {
		return v, fmt.Errorf("开卡模板过多")
	}
	seen := map[string]bool{}
	for i := range v.Templates {
		t := &v.Templates[i]
		platform, err := cardplatform.Normalize(t.Platform)
		if err != nil {
			return v, err
		}
		t.Platform = platform
		if t.PlanCode != "" && t.PlanCode != "pro5" && t.PlanCode != "pro20" {
			return v, fmt.Errorf("模板适用套餐须为 5x 或 20x")
		}
		if t.ID == "" || seen[t.ID] || t.Name == "" || t.ValidDays < 1 || t.ValidDays > 3650 {
			return v, fmt.Errorf("模板 ID、名称和有效天数无效")
		}
		seen[t.ID] = true
		input := t.Input(time.Now())
		if e := ValidateOpen(input, time.Now()); e != nil {
			return v, fmt.Errorf("模板 %s: %w", t.Name, e)
		}
	}
	return v, nil
}
func (t Template) Input(now time.Time) OpenInput {
	v := t.OpenInput
	v.ActiveDate = now.In(time.FixedZone("CST", 8*3600)).AddDate(0, 0, t.ValidDays).Format("2006-01-02")
	return v
}
func ValidateOpen(v OpenInput, now time.Time) error {
	if strings.TrimSpace(v.CardType) == "" || len(v.CardType) > 100 {
		return fmt.Errorf("请填写卡台已授权的卡类型")
	}
	if e := Positive(v.CardAmount); e != nil {
		return e
	}
	if !ValidCurrency(v.CardCurrency) || !ValidCurrency(v.BillCurrency) {
		return fmt.Errorf("请选择三位币种代码")
	}
	if v.UseTime != 0 && v.UseTime != 1 {
		return fmt.Errorf("使用方式无效")
	}
	switch v.MCC {
	case "flight", "hotel", "ecom", "trv":
	default:
		return fmt.Errorf("风控订单类别无效：请选择 flight（机票）、hotel（酒店）、ecom（跨境电商）或 trv（非酒店机票）")
	}
	d, e := time.Parse("2006-01-02", v.ActiveDate)
	today, _ := time.Parse("2006-01-02", now.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02"))
	if e != nil || !d.After(today) {
		return fmt.Errorf("到期日期须晚于今天")
	}
	if len([]rune(v.Note)) > 100 {
		return fmt.Errorf("备注不可超过 100 字符")
	}
	if v.CompanyRate != "" {
		n, e := Money(v.CompanyRate)
		if e != nil || n.Cmp(big.NewRat(1, 1)) > 0 {
			return fmt.Errorf("汇率加成应为 0～1 的小数")
		}
	}
	if v.AuthCount != nil && *v.AuthCount < 1 {
		return fmt.Errorf("授权次数必须大于 0")
	}
	return nil
}
