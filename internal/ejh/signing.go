package ejh

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func parseSigningKey(value string) (*rsa.PrivateKey, error) {
	invalid := errors.New("EJH 签名私钥须为 2048～8192 位 RSA PKCS#8，支持 PEM 或 Base64")
	if len(value) > 16384 {
		return nil, invalid
	}
	value = strings.TrimSpace(value)
	var der []byte
	if strings.HasPrefix(value, "-----BEGIN") {
		block, rest := pem.Decode([]byte(value))
		if block == nil || block.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
			return nil, invalid
		}
		der = block.Bytes
	} else {
		var err error
		der, err = base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, value))
		if err != nil {
			return nil, invalid
		}
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, invalid
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.Validate() != nil {
		return nil, invalid
	}
	return key, nil
}

// Private keys never form part of the public settings JSON.
func NormalizeSigningKey(value string) (private, public string, err error) {
	key, err := parseSigningKey(value)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", errors.New("EJH 签名私钥格式无效")
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", errors.New("EJH 公钥生成失败")
	}
	return base64.StdEncoding.EncodeToString(der), base64.StdEncoding.EncodeToString(pub), nil
}

func GenerateSigningKey() (private, public string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", errors.New("EJH 签名密钥生成失败")
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", errors.New("EJH 签名密钥生成失败")
	}
	return NormalizeSigningKey(base64.StdEncoding.EncodeToString(der))
}

func signRSA(value, private string) (string, error) {
	key, err := parseSigningKey(private)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(value))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", errors.New("EJH 请求签名失败")
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func authenticationBody(cfg Settings, secret string) (map[string]any, error) {
	body := map[string]any{"appKey": cfg.AppKey}
	if cfg.SigningPrivateKey == "" {
		return body, nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("EJH 认证随机数生成失败")
	}
	ts := time.Now().Unix()
	url := cfg.URL + "/api/v3/authenticate"
	random := hex.EncodeToString(nonce)
	sign, err := signRSA("appsecret="+secret+"&nonce="+random+"&timestamp="+strconv.FormatInt(ts, 10)+"&url="+url, cfg.SigningPrivateKey)
	if err != nil {
		return nil, err
	}
	body["timestamp"], body["nonce"], body["url"], body["sign"] = ts, random, url, sign
	return body, nil
}
