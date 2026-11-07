package ejh

import (
	"context"
	"crypto"
	"crypto/des"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func signingFixture(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	private, public, err := GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(public)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	return private, key.(*rsa.PublicKey)
}

func verifyFixture(t *testing.T, public *rsa.PublicKey, value, signature string) {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Error("invalid signature encoding")
		return
	}
	hash := sha256.Sum256([]byte(value))
	if rsa.VerifyPKCS1v15(public, crypto.SHA256, hash[:], decoded) != nil {
		t.Error("RSA verification failed")
	}
}

func TestSigningAllUsedBusinessMethods(t *testing.T) {
	private, public := signingFixture(t)
	secret := "fixture-secret&=中文 +/%"
	cases := []struct {
		method   string
		data     map[string]any
		response any
	}{
		{"card.open.info", map[string]any{"cardType": "VISA_C1", "cardAmount": "200", "cardCurrency": "USD", "billCurrency": "USD", "useTime": 0, "companyRate": "0.01", "authCount": 10, "activeDate": "2027-12-31", "supportedMccGroup": "trv"}, Card{OrderNo: "fixture", Status: "1", Validity: "1227"}},
		{"card.cancel.info", map[string]any{"orderNo": "fixture"}, map[string]any{"orderNo": "fixture", "status": "SUCCESS"}},
		{"card.query.info", map[string]any{"orderNo": "fixture"}, Card{OrderNo: "fixture", Status: "2"}},
		{"account.balance.info", map[string]any{}, map[string]any{"abroadList": []any{map[string]any{"currency": "USD", "balance": "20.00"}}}},
		{"card.balance.info", map[string]any{"orderNo": "fixture"}, map[string]any{"balance": "10.00"}},
		{"card.list.info", map[string]any{"page": 1, "size": 100}, map[string]any{"total": 0, "cardInfos": []any{}}},
		{"card.trade.info", map[string]any{"orderNo": "fixture", "page": 1, "size": 100, "tradeTime": "2026-11-01 00:00:00", "endTime": "2026-11-07 23:59:59"}, map[string]any{"totalSize": 0, "tradeInfos": []any{}}},
		{"card.change.info", map[string]any{"orderNo": "fixture", "cardAmount": "100"}, map[string]any{"orderNo": "fixture", "billAmount": "300.00"}},
	}
	authCalls, businessCalls := 0, 0
	nonces := map[string]bool{}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), private) {
			t.Error("secret transmitted")
		}
		if r.Method != "POST" {
			t.Error("wrong method")
		}
		if r.URL.Path == "/api/v3/authenticate" {
			authCalls++
			var body struct {
				AppKey           string `json:"appKey"`
				Timestamp        int64  `json:"timestamp"`
				Nonce, URL, Sign string
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Error(err)
				return
			}
			if body.AppKey != "fixture" || body.URL != "https://"+r.Host+"/api/v3/authenticate" || time.Now().Unix()-body.Timestamp > 5 || body.Timestamp > time.Now().Unix()+1 || !regexp.MustCompile(`^[a-zA-Z0-9]{16,32}$`).MatchString(body.Nonce) || nonces[body.Nonce] {
				t.Error("invalid authentication fields")
			}
			nonces[body.Nonce] = true
			verifyFixture(t, public, fmt.Sprintf("appsecret=%s&nonce=%s&timestamp=%d&url=%s", secret, body.Nonce, body.Timestamp, body.URL), body.Sign)
			// No cache: verify each authentication generates a fresh nonce.
			json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": map[string]any{"authToken": "fixture-token", "validity": 0}})
			return
		}
		if r.URL.Path != "/api/v3/card" || r.Header.Get("Authorization") != "fixture-token" {
			t.Error("wrong business endpoint/header")
		}
		var body map[string]string
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
			return
		}
		if len(body) != 3 || body["appKey"] != "fixture" {
			t.Error("wrong business envelope")
		}
		verifyFixture(t, public, body["param"], body["sign"])
		ciphertext, err := base64.StdEncoding.DecodeString(body["param"])
		if err != nil || len(ciphertext) == 0 || len(ciphertext)%8 != 0 {
			t.Error("bad DES ciphertext")
			return
		}
		block, _ := des.NewCipher([]byte(secret)[:8])
		plain := make([]byte, len(ciphertext))
		for i := 0; i < len(plain); i += 8 {
			block.Decrypt(plain[i:i+8], ciphertext[i:i+8])
		}
		pad := int(plain[len(plain)-1])
		if pad < 1 || pad > 8 {
			t.Error("bad padding")
			return
		}
		for _, v := range plain[len(plain)-pad:] {
			if int(v) != pad {
				t.Error("bad PKCS5 padding")
			}
		}
		var decoded struct {
			Method    string
			Timestamp int64
			V         string
			Data      map[string]any
		}
		if json.Unmarshal(plain[:len(plain)-pad], &decoded) != nil {
			t.Error("bad business JSON")
			return
		}
		tc := cases[businessCalls]
		businessCalls++
		want, _ := json.Marshal(tc.data)
		got, _ := json.Marshal(decoded.Data)
		if decoded.Method != tc.method || decoded.V != "3.0" || time.Now().Unix()-decoded.Timestamp > 5 || string(want) != string(got) {
			t.Error("business payload changed")
		}
		json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": tc.response})
	}))
	defer up.Close()
	c := New()
	c.HTTP = up.Client()
	cfg := Settings{URL: up.URL, AppKey: "fixture", SigningKeyPresent: true, SigningPrivateKey: private}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			var got any
			if err := c.Call(context.Background(), cfg, secret, tc.method, tc.data, &got); err != nil {
				t.Fatal(err)
			}
			wantRaw, _ := json.Marshal(tc.response)
			var want any
			json.Unmarshal(wantRaw, &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatal("response changed")
			}
		})
	}
	if authCalls != 8 || businessCalls != 8 {
		t.Fatal("missing/unexpected request")
	}
}

func TestSigningKeyFormatsAndFailClosed(t *testing.T) {
	private, _ := signingFixture(t)
	der, _ := base64.StdEncoding.DecodeString(private)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	for _, input := range []string{private, pemKey, " \n" + private[:50] + "\n" + private[50:] + " \n"} {
		got, _, err := NormalizeSigningKey(input)
		if err != nil || got != private {
			t.Fatal("valid format rejected", err)
		}
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ec)
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	weakDER, _ := x509.MarshalPKCS8PrivateKey(weak)
	for _, input := range []string{"", "fixture-invalid", pemKey + "trailing", strings.Replace(pemKey, "PRIVATE KEY", "RSA PRIVATE KEY", -1), base64.StdEncoding.EncodeToString(ecDER), base64.StdEncoding.EncodeToString(weakDER), strings.Repeat("a", 16385)} {
		if _, _, err := NormalizeSigningKey(input); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	up := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid key must not send request") }))
	defer up.Close()
	c := New()
	c.HTTP = up.Client()
	for _, key := range []string{"", "invalid-private-fixture"} {
		_, err := c.Token(context.Background(), Settings{URL: up.URL, AppKey: "fixture", SigningKeyPresent: true, SigningPrivateKey: key}, "fixture-secret")
		if err == nil || strings.Contains(err.Error(), "invalid-private-fixture") {
			t.Fatal("invalid key did not fail safely")
		}
	}
}

func TestSigningTokenRefreshAndMutationNotReplayed(t *testing.T) {
	private, public := signingFixture(t)
	for _, method := range []string{"card.list.info", "card.open.info", "card.cancel.info", "card.change.info"} {
		t.Run(method, func(t *testing.T) {
			authCalls, calls := 0, 0
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if r.URL.Path == "/api/v3/authenticate" {
					authCalls++
					if body["sign"] == nil {
						t.Error("unsigned reauthentication")
					}
					json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": map[string]any{"authToken": fmt.Sprint(authCalls), "validity": 60000}})
					return
				}
				calls++
				verifyFixture(t, public, body["param"].(string), body["sign"].(string))
				w.Write([]byte(`{"code":"000009","msg":"token已过期"}`))
			}))
			defer up.Close()
			c := New()
			c.HTTP = up.Client()
			cfg := Settings{URL: up.URL, AppKey: "fixture", SigningKeyPresent: true, SigningPrivateKey: private}
			if c.Call(context.Background(), cfg, "fixture-secret", method, map[string]any{}, nil) == nil {
				t.Fatal("error hidden")
			}
			want := 1
			if method == "card.list.info" {
				want = 2
			}
			if authCalls != want || calls != want {
				t.Fatal("retry count", authCalls, calls)
			}
		})
	}
	other, _ := signingFixture(t)
	a := Settings{URL: "https://fixture.invalid", AppKey: "fixture", SigningPrivateKey: private}
	b := a
	b.SigningPrivateKey = other
	if tokenScope(a, "secret") == tokenScope(b, "secret") {
		t.Fatal("key change reuses token")
	}
}
