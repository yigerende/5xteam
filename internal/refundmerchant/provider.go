package refundmerchant

import (
	"errors"
	"strings"
)

const (
	ProviderOriginal    = "original"
	ProviderStarBridge  = "starbridge"
	StarBridgeSubmitURL = "https://bridge.ccgpai.com/api/v1/refund/orders"
)

func NormalizeProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", ProviderOriginal:
		return ProviderOriginal, nil
	case ProviderStarBridge:
		return ProviderStarBridge, nil
	default:
		return "", errors.New("请选择有效的 API 商户供应商")
	}
}

func NormalizeProviderURL(provider, endpoint string) (string, error) {
	provider, err := NormalizeProvider(provider)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(endpoint) == "" && provider == ProviderStarBridge {
		endpoint = StarBridgeSubmitURL
	}
	return NormalizeSubmitURL(endpoint)
}
