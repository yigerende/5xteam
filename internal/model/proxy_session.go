package model

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Empty vendor preserves an existing raw/static proxy. Configured vendors use
// a fresh sticky session per login client, never per HTTP request.
type ProxySessionConfig struct {
	Vendor         string `json:"vendor"`
	Country        string `json:"country"`
	SessionMinutes int    `json:"session_minutes"`
}

func (c ProxySessionConfig) Normalized() ProxySessionConfig {
	c.Vendor = strings.ToLower(strings.TrimSpace(c.Vendor))
	c.Country = strings.ToUpper(strings.TrimSpace(c.Country))
	if c.Country == "" {
		c.Country = "PH"
	}
	if c.SessionMinutes == 0 {
		c.SessionMinutes = 60
	}
	return c
}

func (c ProxySessionConfig) Validate(endpoint string) error {
	c = c.Normalized()
	if c.Vendor == "" {
		return nil
	}
	minimum, maximum := 1, 10080
	switch c.Vendor {
	case "iproyal", "other":
	case "cliproxy", "1024proxy":
		maximum = 120
	case "711proxy":
		minimum, maximum = 5, 180
	default:
		return errors.New("不支持的代理厂商")
	}
	if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(c.Country) {
		return errors.New("代理国家须填写两位国家代码，例如 PH（菲律宾）")
	}
	if c.SessionMinutes < minimum || c.SessionMinutes > maximum {
		return errors.New("会话分钟数超出厂商范围")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.User == nil {
		return errors.New("厂商会话代理需要账号和密码")
	}
	p, ok := u.User.Password()
	if !ok || p == "" || u.User.Username() == "" {
		return errors.New("厂商会话代理需要账号和密码")
	}
	if c.Vendor == "other" {
		for _, key := range []string{"{country}", "{session}", "{minutes}"} {
			if !strings.Contains(u.User.Username()+":"+p, key) {
				return errors.New("自定义厂商须在账号或密码中包含 {country}、{session}、{minutes}")
			}
		}
	}
	return nil
}
