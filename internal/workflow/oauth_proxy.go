package workflow

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var iproyalSessionOption = regexp.MustCompile(`(?i)_session-[^_]*`)

// PrepareIPRoyalOAuthProxy is called once before an attempt's quality checks.
// Keep its result for the entire login; the configured URL remains unchanged.
func PrepareIPRoyalOAuthProxy(proxyURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || !strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "geo.iproyal.com") {
		return proxyURL, nil
	}
	password, hasPassword := parsed.User.Password()
	if parsed.User.Username() == "" || !hasPassword || password == "" {
		return "", errors.New("IPRoyal OAuth 代理需要用户名和密码")
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", errors.New("无法生成 IPRoyal OAuth 代理会话")
	}
	session := "_session-" + hex.EncodeToString(random[:])
	if !strings.Contains(strings.ToLower(password), "_country-") {
		if index := iproyalSessionOption.FindStringIndex(password); index != nil {
			password = password[:index[0]] + "_country-us" + password[index[0]:]
		} else {
			password += "_country-us"
		}
	}
	if iproyalSessionOption.MatchString(password) {
		password = iproyalSessionOption.ReplaceAllString(password, session)
	} else {
		password += session
	}
	parsed.User = url.UserPassword(parsed.User.Username(), password)
	return parsed.String(), nil
}
