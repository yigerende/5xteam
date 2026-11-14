package workflow

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"

	"chatgpt-space-merge/internal/model"
)

var royalOptions = regexp.MustCompile(`(?i)_(?:country|session|lifetime)-[^_]*`)
var cliOptions = regexp.MustCompile(`(?i)-(?:region|sid|t)-[^-]*`)
var sevenOptions = regexp.MustCompile(`(?i)-(?:region|session|sessTime|sessAuto)-[^-]*`)
var sevenAuto = regexp.MustCompile(`(?i)-sessAuto-([^-]*)`)

func PrepareVendorProxy(endpoint string, config model.ProxySessionConfig) (string, error) {
	c := config.Normalized()
	if c.Vendor == "" {
		return PrepareIPRoyalOAuthProxy(endpoint)
	}
	if err := c.Validate(endpoint); err != nil {
		return "", err
	}
	u, _ := url.Parse(endpoint)
	user, password := u.User.Username(), ""
	password, _ = u.User.Password()
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("生成代理会话失败: %w", err)
	}
	session := hex.EncodeToString(random[:])
	switch c.Vendor {
	case "iproyal":
		duration := fmt.Sprintf("%dm", c.SessionMinutes)
		if c.SessionMinutes%60 == 0 {
			duration = fmt.Sprintf("%dh", c.SessionMinutes/60)
		}
		password = royalOptions.ReplaceAllString(password, "") + fmt.Sprintf("_country-%s_session-%s_lifetime-%s", strings.ToLower(c.Country), session, duration)
	case "cliproxy", "1024proxy":
		user = cliOptions.ReplaceAllString(user, "") + fmt.Sprintf("-region-%s-sid-%s-t-%d", c.Country, session, c.SessionMinutes)
	case "711proxy":
		n, err := rand.Int(rand.Reader, big.NewInt(100000000))
		if err != nil {
			return "", fmt.Errorf("生成代理会话失败: %w", err)
		}
		auto := sevenAuto.FindStringSubmatch(user)
		user = sevenOptions.ReplaceAllString(user, "") + fmt.Sprintf("-region-%s-session-%08d-sessTime-%d", c.Country, n.Int64(), c.SessionMinutes)
		if len(auto) > 1 {
			user += "-sessAuto-" + auto[1]
		}
	case "other":
		r := strings.NewReplacer("{country}", c.Country, "{session}", session, "{minutes}", fmt.Sprint(c.SessionMinutes))
		user, password = r.Replace(user), r.Replace(password)
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}
