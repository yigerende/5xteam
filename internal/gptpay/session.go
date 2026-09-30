package gptpay

import "encoding/json"

// Preserve the web session fields supplied by /api/auth/session, while keeping
// local OAuth refresh tokens and cookie credentials out of payment requests.
func (s *Session) UnmarshalJSON(data []byte) error {
	type plain Session
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = Session(v)
	s.Raw = append(json.RawMessage(nil), data...)
	return nil
}

func (s Session) MarshalJSON() ([]byte, error) {
	type plain Session
	if len(s.Raw) == 0 {
		return json.Marshal(plain(s))
	}
	v := map[string]json.RawMessage{}
	if len(s.Raw) > 0 {
		if err := json.Unmarshal(s.Raw, &v); err != nil {
			return nil, err
		}
	}
	if v == nil {
		v = map[string]json.RawMessage{}
	}
	for _, k := range []string{"access_token", "refresh_token", "refreshToken", "id_token", "idToken", "session_token", "sessionToken", "oauth_token_type", "oauth_scope", "expires_at"} {
		delete(v, k)
	}
	for key, fields := range map[string]map[string]string{"user": {"email": s.User.Email}, "account": {"id": s.Account.ID}} {
		nested := map[string]json.RawMessage{}
		_ = json.Unmarshal(v[key], &nested)
		if nested == nil {
			nested = map[string]json.RawMessage{}
		}
		for k, value := range fields {
			nested[k], _ = json.Marshal(value)
		}
		v[key], _ = json.Marshal(nested)
	}
	v["accessToken"], _ = json.Marshal(s.AccessToken)
	// Keep the original three-field encoding for legacy encrypted snapshots.
	var user, account map[string]json.RawMessage
	_ = json.Unmarshal(v["user"], &user)
	_ = json.Unmarshal(v["account"], &account)
	if len(v) == 3 && len(user) == 1 && len(account) == 1 {
		return json.Marshal(plain(s))
	}
	return json.Marshal(v)
}
